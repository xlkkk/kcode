// agenttool.go — Agent 工具（子 Agent / teammate 派发）的装配。
//
// 对应移植计划「集成更新 · agent.go」：把 agents.AgentTool 需要的运行时
// 组件（Loader / TaskMgr / 权限资源 / Worktree 钩子 / SpawnTeammate 钩子）
// 在主 Agent 组装时接线。Agent 工具必须最后注册 —— 它的工具池是此前注册
// 的全部工具，子 Agent 才能拿到与父一致的（过滤后）能力。

package agent

import (
	"context"
	"fmt"

	"kcode/internal/agents"
	"kcode/internal/middleware"
	"kcode/internal/teams"
	"kcode/internal/worktree"

	"github.com/cloudwego/eino/adk"
)

func registerAgentTool(ctx context.Context, opts AgentOptions) error {
	loader := opts.AgentLoader
	if loader == nil {
		loader = agents.NewAgentLoader(opts.WorkDir)
	}
	taskMgr := opts.TaskMgr
	if taskMgr == nil {
		taskMgr = agents.NewTaskManager()
	}

	agentTool := agents.NewAgentTool()
	agentTool.ChatModel = opts.ChatModel
	agentTool.ModelResolver = opts.ModelResolver
	agentTool.SystemPrompt = opts.SystemPrompt
	agentTool.Loader = loader
	agentTool.TaskMgr = taskMgr
	agentTool.ProgressCh = opts.ProgressCh
	agentTool.Sandbox = opts.Sandbox
	agentTool.RuleEngine = opts.RuleEngine
	agentTool.PermMode = opts.PermMode

	// 子 Agent 额外中间件：文件快照跟踪（与主链共用同一个 History 实例）。
	if opts.FileHistory != nil {
		agentTool.ExtraHandlers = append(agentTool.ExtraHandlers, middleware.NewFileTrackMW(opts.FileHistory))
	}

	// Worktree 隔离钩子（isolation="worktree" 时消费）。
	agentTool.WT = &agents.WorktreeHooks{
		Create: func(ctx context.Context, slug string) (string, string, string, string, error) {
			r, err := worktree.CreateAgentWorktree(ctx, slug)
			if err != nil {
				return "", "", "", "", err
			}
			return r.WorktreePath, r.WorktreeBranch, r.GitRoot, r.HeadCommit, nil
		},
		HasChanges: func(ctx context.Context, worktreePath, headCommit string) bool {
			return worktree.HasWorktreeChanges(ctx, worktreePath, headCommit)
		},
		Remove: func(ctx context.Context, worktreePath, branch, gitRoot string) error {
			if !worktree.RemoveAgentWorktree(ctx, worktreePath, branch, gitRoot) {
				return fmt.Errorf("failed to remove worktree at %s (see stderr)", worktreePath)
			}
			return nil
		},
		Notice: worktree.BuildWorktreeNotice,
	}

	// teammate 派发钩子：先把 Lead 绑定的协作工具换成成员绑定版，再 spawn。
	if opts.TeamMgr != nil {
		teamMgr := opts.TeamMgr
		agentTool.SpawnTeammate = func(ctx context.Context, req agents.TeammateSpawnRequest) (string, error) {
			req.Tools = teams.BindTeammateTools(ctx, req.Tools, teamMgr, req.TeamName, req.MemberName)
			return teamMgr.SpawnTeammate(ctx, req)
		}
	}

	// 工具池快照在此刻定格：AskUser / Worktree 等已注册，Agent 工具自身不需要在池里
	// （子 Agent 默认拿不到 Agent，fork 路径的浅拷贝另有 cloneToolsForFork 处理）。
	agentTool.AllTools = opts.ToolRegistry.GetAll()
	opts.ToolRegistry.Register(agentTool)

	// fork 数据源：ParentStateMW 每轮把主 Agent 的对话快照进 AgentTool。
	opts.MWRegistry.Register(adk.ChatModelAgentMiddleware(agents.NewParentStateMW(agentTool)))

	return nil
}
