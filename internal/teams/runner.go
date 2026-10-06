// runner.go — in-process 队友主循环（Eino 版）。
//
// 备注 internal/teams/inprocess.go + runner.go
// 差异：队友执行从 agent.Agent.Run + conversation.Manager 换成每轮一个
// adk.Runner.Run；跨轮记忆靠 Member.History（[]*schema.Message）累积；
// 去掉 plan 审批分支（kcode 不实现 plan 模式）。

package teams

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// IdlePollInterval is how often an idle teammate scans its inbox for new work.
const IdlePollInterval = 500 * time.Millisecond

// RunInProcessTeammate drives a teammate's main loop in the current process.
// It blocks until ctx is cancelled or a shutdown request lands in the inbox.
// Each iteration:
//
//  1. fold pending mailbox messages into a system-reminder user message;
//  2. run one agent turn over the accumulated history, forwarding progress
//     into Member.Progress;
//  3. drop an idle notification into the lead's inbox so it can dispatch
//     the next task;
//  4. idle-poll the inbox for the next prompt or a shutdown request.
func RunInProcessTeammate(
	ctx context.Context,
	team *Team,
	member *Member,
	initialPrompt string,
	addendum string,
) error {
	nextPrompt := initialPrompt
	firstTurn := true
	idleReason := "available"

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// 身份提示只在第一轮注入；信箱里新到的消息每轮折叠成一条
		// system-reminder，让模型把它当成入站通知而不是用户指令。
		var reminders []string
		if firstTurn && addendum != "" {
			reminders = append(reminders, addendum)
		}
		if reminder := InjectPendingMessages(team, member.Name); reminder != "" {
			reminders = append(reminders, reminder)
		}

		inputs := make([]*schema.Message, 0, 3)
		if len(reminders) > 0 {
			inputs = append(inputs, schema.UserMessage("<system-reminder>\n"+strings.Join(reminders, "\n\n")+"\n</system-reminder>"))
		}
		if nextPrompt != "" {
			inputs = append(inputs, schema.UserMessage(nextPrompt))
		}
		nextPrompt = ""
		firstTurn = false

		text, err := runTeammateTurn(ctx, member, inputs)
		if err != nil {
			idleReason = "failed"
		}
		if member.Progress != nil {
			if text != "" {
				member.Progress.SetLastMessage(truncate(text, 200))
			}
			if idleReason == "failed" {
				member.Progress.SetStatus("failed")
			} else {
				member.Progress.SetStatus("idle")
			}
		}

		// 通知 Lead 这一轮跑完了，Lead 据此决定是否喂下一份活。
		_ = team.MailBox.Send(LeadName, CreateIdleNotification(member.Name, idleReason))
		idleReason = "available"

		// 空闲轮询：睡 IdlePollInterval 后翻信箱。见关闭消息收工，
		// 否则把新消息拼成下一轮 prompt 回到循环顶部。
		prompt, shutdown, err := waitForNextPromptOrShutdown(ctx, team, member.Name)
		if err != nil {
			return err
		}
		if shutdown != nil {
			// 收工前给 Lead 一个明确答复。队友此刻在空闲轮询，手上没有
			// 干到一半的活，一律同意；干活干到一半被打断的队友轮询不到这条。
			if shutdown.Type == MsgShutdownRequest {
				_ = team.MailBox.Send(LeadName,
					NewShutdownResponse(member.Name, shutdown.RequestID, true, "acknowledged, shutting down"))
			}
			return nil
		}
		nextPrompt = prompt
	}
}

// runTeammateTurn 跑一轮：历史 + 本轮输入喂给 Runner，产出消息全部回填
// 历史（assistant 的 tool_calls 和 tool 结果必须成对留存，下一轮模型才
// 看得懂自己干过什么）。
func runTeammateTurn(ctx context.Context, member *Member, inputs []*schema.Message) (string, error) {
	agent, err := member.buildAgent(ctx)
	if err != nil {
		return "", err
	}

	member.History = append(member.History, inputs...)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	iter := runner.Run(ctx, member.History)

	var (
		text     strings.Builder
		firstErr error
		emitted  []*schema.Message
	)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil && firstErr == nil {
			firstErr = ev.Err
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput
		var msg *schema.Message
		if mv.IsStreaming && mv.MessageStream != nil {
			completed, err := schema.ConcatMessageStream(mv.MessageStream)
			if err != nil || completed == nil {
				continue
			}
			msg = completed
		} else if mv.Message != nil {
			msg = mv.Message
		}
		if msg == nil {
			continue
		}
		emitted = append(emitted, msg)

		if msg.Role == schema.Assistant {
			if msg.Content != "" {
				text.WriteString(msg.Content)
			}
			if member.Progress != nil {
				for _, tc := range msg.ToolCalls {
					member.Progress.RecordToolUse(tc.Function.Name, nil)
				}
			}
		}
	}
	member.History = append(member.History, emitted...)
	return text.String(), firstErr
}

// buildAgent 按成员携带的 Eino 配置构建 ChatModelAgent。每轮重建一次，
// 配置不变所以行为稳定；Runner 才是每轮新建的对象。
func (m *Member) buildAgent(ctx context.Context) (*adk.ChatModelAgent, error) {
	maxIter := m.MaxTurns
	if maxIter <= 0 {
		maxIter = 200
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "teammate:" + m.Name,
		Model:         m.ChatModel,
		Instruction:   m.Instruction,
		MaxIterations: maxIter,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: m.Tools,
			},
		},
		Handlers: m.Handlers,
	})
}

// BuildTeammateAddendum creates the system-reminder text injected at the top
// of a teammate's first turn. It tells the model its identity, who else is on
// the team, and how to send messages.
func BuildTeammateAddendum(teamName, memberName string, otherMembers []string) string {
	var sb strings.Builder
	sb.WriteString("You are a member of team \"" + teamName + "\". Your name is \"" + memberName + "\".\n\n")
	sb.WriteString("The lead is reachable as \"" + LeadName + "\". Deliver your final result to the lead with SendMessage(to=\"" + LeadName + "\", content=...) — the idle notification alone only signals completion, it does not carry your output.\n")
	if len(otherMembers) > 0 {
		sb.WriteString("Other team members: " + strings.Join(otherMembers, ", ") + "\n")
	}
	sb.WriteString("\nYou can communicate with the lead and teammates using the SendMessage tool.\n")
	sb.WriteString("Messages from the team arrive as system reminders at the start of each turn.\n")
	sb.WriteString("When you finish your current task, send your final result to \"" + LeadName + "\" via SendMessage, then stop calling tools — an idle notification will be sent to the lead automatically.\n")
	return sb.String()
}

// InjectPendingMessages returns any unread mailbox messages formatted as a
// system-reminder string and marks them read. The empty return means no new
// mail.
func InjectPendingMessages(team *Team, memberName string) string {
	msgs, err := team.MailBox.ReadUnread(memberName)
	if err != nil || len(msgs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("You have new messages:\n\n")
	for _, msg := range msgs {
		if IsShutdownRequest(msg) {
			continue // 关闭消息由空闲轮询处理，不喂给模型
		}
		sb.WriteString("From " + msg.From + ": " + msg.Text + "\n\n")
	}

	_ = team.MailBox.MarkAllRead(memberName)
	return sb.String()
}

// waitForNextPromptOrShutdown blocks until the inbox has at least one message,
// then turns the unread batch into the next user prompt. If any message is a
// shutdown request it returns that message without building a prompt.
func waitForNextPromptOrShutdown(ctx context.Context, team *Team, memberName string) (string, *FileMailMessage, error) {
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(IdlePollInterval):
		}

		msgs, err := team.MailBox.ReadUnread(memberName)
		if err != nil {
			return "", nil, err
		}
		if len(msgs) == 0 {
			continue
		}

		var shutdown *FileMailMessage
		var keep []FileMailMessage
		for i, m := range msgs {
			if IsShutdownRequest(m) {
				shutdown = &msgs[i]
				continue
			}
			keep = append(keep, m)
		}
		_ = team.MailBox.MarkAllRead(memberName)

		if shutdown != nil {
			return "", shutdown, nil
		}
		return formatInboundAsPrompt(keep), nil, nil
	}
}

// DrainLeadMailbox reads every unread notification in every team's lead inbox
// and returns them as system-reminder strings (one per team). The lead's
// session installs this in its reminder middleware so teammate idle
// notifications surface to the model at the top of each turn.
func DrainLeadMailbox(mgr *TeamManager) []string {
	if mgr == nil {
		return nil
	}
	var notes []string
	for _, name := range mgr.ListTeams() {
		team := mgr.GetTeam(name)
		if team == nil {
			continue
		}
		msgs, err := team.MailBox.ReadUnread(LeadName)
		if err != nil || len(msgs) == 0 {
			continue
		}
		var sb strings.Builder
		sb.WriteString("<team-notification team=\"")
		sb.WriteString(name)
		sb.WriteString("\">\n")
		for _, m := range msgs {
			sb.WriteString("from=")
			sb.WriteString(m.From)
			sb.WriteString(": ")
			sb.WriteString(m.Text)
			sb.WriteString("\n")
		}
		sb.WriteString("</team-notification>")
		notes = append(notes, sb.String())
		_ = team.MailBox.MarkAllRead(LeadName)
	}
	return notes
}

// formatInboundAsPrompt turns an unread batch into a single user prompt. Each
// message is tagged with its sender so the teammate can route a reply.
func formatInboundAsPrompt(msgs []FileMailMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("You have new messages from your team:\n\n")
	for _, m := range msgs {
		sb.WriteString(fmt.Sprintf("From %s: %s\n\n", m.From, m.Text))
	}
	return sb.String()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
