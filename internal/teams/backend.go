// backend.go — 团队后端（in-process 简化版）。
//
// 备注 internal/teams/backend.go + spawn.go
// 差异：只保留 ModeInProcess。tmux / iTerm 窗格后端是 CLI/TUI 专属，
// kcode 的交互走 Web 端，队友统一用 goroutine + Eino ChatModelAgent 运行。

package teams

import (
	"context"
	"fmt"

	"kcode/internal/agents"
)

type TeamMode string

const (
	ModeInProcess TeamMode = "in-process"
)

// DetectBackend 保留函数形态给调用方（TeamCreate 工具）：kcode 恒为
// in-process，不做 TMUX / ITERM_SESSION_ID 环境变量探测。
func DetectBackend() TeamMode { return ModeInProcess }

// SpawnTeammate 是 agents.AgentTool.SpawnTeammate 钩子的实现，签名与
// agents.TeammateSpawnRequest 直接对齐（集成阶段一行赋值接线）。
//
// 团队不存在时按 Lead 指定的名字自动建；成员重名直接报错，让模型换名字
// 重试，而不是悄悄覆盖一个在跑的队友。
func (tm *TeamManager) SpawnTeammate(ctx context.Context, req agents.TeammateSpawnRequest) (string, error) {
	if req.TeamName == "" || req.MemberName == "" {
		return "", fmt.Errorf("SpawnTeammate: team name and member name are required")
	}

	team := tm.GetTeam(req.TeamName)
	if team == nil {
		team = tm.CreateTeamFull(req.TeamName, ModeInProcess, LeadName, req.Description)
	}
	if m := team.Members[req.MemberName]; m != nil && m.Active {
		return "", fmt.Errorf("member %q is already active in team %q", req.MemberName, req.TeamName)
	}

	addendum := BuildTeammateAddendum(team.Name, req.MemberName, otherMemberNames(team, req.MemberName))
	StartInProcessMember(ctx, team, req, addendum)

	return fmt.Sprintf("Teammate \"%s\" joined team \"%s\" (mode: %s). It starts on the task now; "+
		"progress and its final result will arrive via team notifications — reply with SendMessage(to=\"%s\") if you need to follow up.",
		req.MemberName, team.Name, team.Mode, req.MemberName), nil
}

// StartInProcessMember 注册成员并在后台 goroutine 里启动它的主循环。
// goroutine 生命周期绑定 ctx：调用方取消 ctx 即停掉队友。
func StartInProcessMember(ctx context.Context, team *Team, req agents.TeammateSpawnRequest, addendum string) *Member {
	member := team.AddMember(req.MemberName, req.ChatModel, req.Tools, req.Instruction, req.Handlers, req.MaxTurns)
	member.Progress = NewTeammateProgress(member.Name, team.Name)
	team.SetMemberMeta(req.MemberName, req.SubagentType, req.Spec.Model, "")

	memberCtx, cancel := context.WithCancel(ctx)
	team.mu.Lock()
	member.Active = true
	member.Cancel = cancel
	team.persist()
	team.mu.Unlock()

	go func() {
		defer func() {
			team.mu.Lock()
			member.Active = false
			team.persist()
			team.mu.Unlock()
			if member.Progress != nil && member.Progress.GetStatus() == "running" {
				member.Progress.SetStatus("stopped")
			}
		}()
		_ = RunInProcessTeammate(memberCtx, team, member, req.Prompt, addendum)
	}()
	return member
}

// otherMemberNames 列出团队里除自己以外的成员，用于队友的身份提示。
func otherMemberNames(team *Team, self string) []string {
	team.mu.Lock()
	defer team.mu.Unlock()
	var names []string
	for name := range team.Members {
		if name != self {
			names = append(names, name)
		}
	}
	return names
}
