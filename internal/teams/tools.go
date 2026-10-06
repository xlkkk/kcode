// tools.go — 团队 Eino 工具：SendMessage / TeamCreate / TeamDelete。
//
// 备注 internal/teams/tools.go
// 差异：InvokableTool 版；去掉 plan_approval_response 消息类型（不实现
// plan）；新增 BindTeammateTools —— 集成层在 SpawnTeammate 钩子里把
// Lead 绑定的协作工具换成成员绑定的副本（SendMessage 要知道发送者是谁）。

package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"kcode/internal/todo"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// --- SendMessage ---

// SendMessageTool allows agents to send messages to named teammates.
// SenderName 是构造时绑定的发送者：Lead 会话用 LeadName，每个队友用
// 自己的成员名（见 BindTeammateTools）。
type SendMessageTool struct {
	TeamMgr    *TeamManager
	SenderName string
}

var _ tool.InvokableTool = &SendMessageTool{}

func NewSendMessageTool(teamMgr *TeamManager, senderName string) *SendMessageTool {
	return &SendMessageTool{TeamMgr: teamMgr, SenderName: senderName}
}

func (t *SendMessageTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "SendMessage",
		Desc: "Send a message to another named agent in the team. The recipient will see it on their next turn.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"to": {
				Type:     schema.String,
				Desc:     "Name of the recipient agent, \"lead\", or \"*\" to broadcast to your teammates",
				Required: true,
			},
			"content": {
				Type:     schema.String,
				Desc:     "Message content to send. For shutdown_request this is the reason; for shutdown_response this is your reply.",
				Required: true,
			},
			"type": {
				Type: schema.String,
				Desc: fmt.Sprintf("Message kind, defaults to 'text'. Use '%s' to ask a teammate to wrap up (it replies with %s).",
					MsgShutdownRequest, MsgShutdownResponse),
				Enum: []string{MsgText, MsgShutdownRequest, MsgShutdownResponse},
			},
			"request_id": {
				Type: schema.String,
				Desc: "Required for shutdown_response: copy the requestId from the shutdown request so the lead can match replies.",
			},
			"approve": {
				Type: schema.Boolean,
				Desc: "Required for shutdown_response: false to refuse because you are still mid-task.",
			},
		}),
	}, nil
}

func (t *SendMessageTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		To        string `json:"to"`
		Content   string `json:"content"`
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Approve   *bool  `json:"approve"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.To == "" || args.Content == "" {
		return "Error: 'to' and 'content' are required", nil
	}

	// 结构化消息走独立通道：它们要带 requestId 和表态，拼进正文的话
	// 收件方还得从自然语言里猜。
	if args.Type != "" && args.Type != MsgText {
		return t.sendTyped(args.To, args.Type, args.RequestID, args.Content, args.Approve), nil
	}

	// 广播：把消息发给发送者所在团队的所有其它成员
	if args.To == "*" {
		team := t.TeamMgr.FindTeamOf(t.SenderName)
		if team == nil {
			return fmt.Sprintf("Error: cannot find team for sender '%s'", t.SenderName), nil
		}
		count := 0
		team.mu.Lock()
		names := make([]string, 0, len(team.Members))
		for name := range team.Members {
			names = append(names, name)
		}
		team.mu.Unlock()
		for _, member := range names {
			if member == t.SenderName {
				continue
			}
			team.SendMessage(t.SenderName, member, args.Content)
			count++
		}
		return fmt.Sprintf("Message broadcast to %d teammate(s).", count), nil
	}

	// Lead 不注册为 Member（它活在主会话里，只读自己的信箱），按发送者
	// 归属的团队把消息投递到 lead 收件箱。
	if args.To == LeadName {
		team := t.TeamMgr.FindTeamOf(t.SenderName)
		if team == nil {
			return fmt.Sprintf("Error: cannot find team for sender '%s'", t.SenderName), nil
		}
		team.SendMessage(t.SenderName, LeadName, args.Content)
		return fmt.Sprintf("Message sent to %s.", LeadName), nil
	}

	// in-process 场景下成员都在同一张 Members 表里，找到归属团队直接投递。
	team := t.TeamMgr.FindTeamOf(t.SenderName)
	if team == nil {
		team = t.TeamMgr.FindTeamOf(args.To)
	}
	if team == nil {
		return fmt.Sprintf("Error: recipient '%s' not found in any team", args.To), nil
	}
	team.mu.Lock()
	_, recipientExists := team.Members[args.To]
	senderExists := t.SenderName == LeadName
	if _, ok := team.Members[t.SenderName]; ok {
		senderExists = true
	}
	team.mu.Unlock()
	if !recipientExists && !senderExists {
		return fmt.Sprintf("Error: recipient '%s' not found in any team", args.To), nil
	}
	team.SendMessage(t.SenderName, args.To, args.Content)
	return fmt.Sprintf("Message sent to %s.", args.To), nil
}

// sendTyped 投递结构化消息。它和普通文本走同一个信箱，区别只在于消息上
// 带了 Type / RequestID 字段，收件方按字段判断，不用解析措辞。
func (t *SendMessageTool) sendTyped(to, msgType, requestID, content string, approve *bool) string {
	var msg FileMailMessage
	switch msgType {
	case MsgShutdownRequest:
		// 关闭请求发给具体成员，收件人从 Text 前缀也能识别
		msg = NewShutdownRequest(t.SenderName, content)
	case MsgShutdownResponse:
		if approve == nil {
			return "Error: shutdown_response requires 'approve'"
		}
		msg = NewShutdownResponse(t.SenderName, requestID, *approve, content)
		if to == "" {
			to = LeadName
		}
	default:
		return "Error: unsupported message type " + msgType
	}

	team := t.TeamMgr.FindTeamOf(t.SenderName)
	if team == nil && to != LeadName {
		team = t.TeamMgr.FindTeamOf(to)
	}
	if team == nil {
		return fmt.Sprintf("Error: cannot find team for sender '%s'", t.SenderName)
	}
	if err := team.MailBox.Send(to, msg); err != nil {
		return "Error sending message: " + err.Error()
	}
	return fmt.Sprintf("%s sent to %s.", msgType, to)
}

// --- TeamCreate ---

// TeamCreateTool creates a new agent team.
type TeamCreateTool struct {
	TeamMgr *TeamManager
}

var _ tool.InvokableTool = &TeamCreateTool{}

func NewTeamCreateTool(teamMgr *TeamManager) *TeamCreateTool {
	return &TeamCreateTool{TeamMgr: teamMgr}
}

func (t *TeamCreateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "TeamCreate",
		Desc: `Create a new team for coordinating multiple agents.

## When to Use

Use this tool proactively whenever:
- The user explicitly asks to use a team, swarm, or group of agents
- The user mentions wanting agents to work together, coordinate, or collaborate
- A task requires sequential or parallel collaboration between multiple agents

When in doubt about whether a task warrants a team, prefer spawning a team.

## Team Workflow

1. **Create a team** with TeamCreate
2. **Spawn teammates** using the Agent tool with team_name and name parameters — this is REQUIRED to create long-running team members
3. Teammates work independently and communicate via **SendMessage**
4. When a teammate finishes, it sends its result to "lead" via SendMessage, then goes idle
5. The lead collects and synthesizes all teammate results

## CRITICAL: Spawning Teammates

To add a member to a team, you MUST pass both team_name and name to the Agent tool:
` + "```" + `
Agent({
  "team_name": "<team name from step 1>",
  "name": "<member name, e.g. reviewer>",
  "prompt": "...",
  "description": "..."
})
` + "```" + `
Without team_name, the agent runs as a one-shot sub-agent that blocks and returns inline — it will NOT be a team member.

## Teammate Idle State

Teammates go idle after every turn — this is completely normal. A teammate going idle after sending a message does NOT mean they are done or unavailable. Sending a message to an idle teammate wakes them up.

## Communication

- Use SendMessage to talk to teammates by name
- Messages from teammates arrive as team notifications at the start of each turn
- Messages are delivered automatically — you do NOT need to manually check your inbox`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"team_name": {
				Type:     schema.String,
				Desc:     "Name for the team",
				Required: true,
			},
			"description": {
				Type: schema.String,
				Desc: "What this team will work on",
			},
		}),
	}, nil
}

func (t *TeamCreateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		TeamName    string `json:"team_name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.TeamName == "" {
		return "Error: team_name is required", nil
	}

	// Deduplicate: if name exists, append suffix
	name := args.TeamName
	for i := 2; t.TeamMgr.GetTeam(name) != nil; i++ {
		name = fmt.Sprintf("%s-%d", args.TeamName, i)
	}

	mode := DetectBackend()
	team := t.TeamMgr.CreateTeamFull(name, mode, LeadName, args.Description)
	out := fmt.Sprintf("Team \"%s\" created (mode: %s). Use Agent tool with team_name=\"%s\" to add teammates.",
		team.Name, team.Mode, team.Name)
	if args.Description != "" {
		out += "\nDescription: " + args.Description
	}
	return out, nil
}

// --- TeamDelete ---

// TeamDeleteTool deletes an agent team and stops all members.
type TeamDeleteTool struct {
	TeamMgr *TeamManager
}

var _ tool.InvokableTool = &TeamDeleteTool{}

func NewTeamDeleteTool(teamMgr *TeamManager) *TeamDeleteTool {
	return &TeamDeleteTool{TeamMgr: teamMgr}
}

func (t *TeamDeleteTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "TeamDelete",
		Desc: "Delete a team, stopping all its members and cleaning up its mailbox.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"team_name": {
				Type:     schema.String,
				Desc:     "Name of the team to delete",
				Required: true,
			},
		}),
	}, nil
}

func (t *TeamDeleteTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		TeamName string `json:"team_name"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.TeamName == "" {
		return "Error: team_name is required", nil
	}

	team := t.TeamMgr.GetTeam(args.TeamName)
	if team == nil {
		return fmt.Sprintf("Error: team '%s' not found", args.TeamName), nil
	}

	team.mu.Lock()
	memberCount := len(team.Members)
	var memberNames []string
	for n := range team.Members {
		memberNames = append(memberNames, n)
	}
	team.mu.Unlock()

	t.TeamMgr.DeleteTeam(args.TeamName)
	return fmt.Sprintf("Team \"%s\" deleted. Stopped %d member(s): %s",
		args.TeamName, memberCount, strings.Join(memberNames, ", ")), nil
}

// --- 集成辅助 ---

// coordinationToolNames 是 BindTeammateTools 需要从工具池里摘掉的协作工具。
var coordinationToolNames = map[string]bool{
	"SendMessage": true,
	"TaskCreate":  true,
	"TaskGet":     true,
	"TaskList":    true,
	"TaskUpdate":  true,
}

// BindTeammateTools 把成员工具池里 Lead 绑定的协作工具换成成员自己绑定的
// 副本：SendMessage 带成员名，Task* 指向团队共享任务板。集成层的
// SpawnTeammate 钩子在派发前调用它。
func BindTeammateTools(ctx context.Context, all []tool.BaseTool, teamMgr *TeamManager, teamName, memberName string) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(all)+5)
	for _, t := range all {
		info, err := t.Info(ctx)
		if err != nil || coordinationToolNames[info.Name] {
			continue
		}
		out = append(out, t)
	}

	list := teamMgr.GetTaskList(teamName)
	out = append(out, NewSendMessageTool(teamMgr, memberName))
	out = append(out,
		todo.NewTaskCreateTool(list),
		todo.NewTaskGetTool(list),
		todo.NewTaskListTool(list),
		todo.NewTaskUpdateTool(list),
	)
	return out
}

// RegisterTeamTools 把 Lead 侧的团队工具注册进主工具池（集成阶段的
// registry 装配调用）。SendMessage 绑定到 LeadName。
func RegisterTeamTools(register func(tool.BaseTool), teamMgr *TeamManager) {
	register(NewSendMessageTool(teamMgr, LeadName))
	register(NewTeamCreateTool(teamMgr))
	register(NewTeamDeleteTool(teamMgr))
}
