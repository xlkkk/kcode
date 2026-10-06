// coordinator.go — 协调者（Lead）模式：工具白名单 + 调度指引。
//
// 备注 internal/teams/coordinator.go
// 差异：白名单去掉 TaskStop / SyntheticOutput（kcode 未实现这两个工具）；
// 保留 AskUserQuestion —— kcode 的 Lead 跑在 Web 会话里，收窄后仍要能向
// 用户提问，否则 HITL 流程会在 Lead 这一环断掉。

package teams

// CoordinatorMode 把 Lead 的工具集收窄到纯调度。
//
// 划线的标准不是「读」和「写」，而是这个工具会不会把大段内容灌进 Lead 的
// 上下文。Lead 的上下文要装任务分解、队员状态和消息记录，一旦它能直接读
// 文件、跑命令，模型就会忍不住自己去查，几千行代码进来，真正该留给调度的
// 空间就没了。所以 read_file / glob / grep / bash 都不在这里：需要看代码
// 就派队员去看，队员把结论带回来，Lead 消化结论、写下一步的规格。
//
// 队员的任务分派靠 Agent 的 prompt 写清楚，不靠共享任务表，因此 TaskCreate /
// TaskGet / TaskList / TaskUpdate 也不给 Lead，这几个是队员之间协调用的，
// 注册在队员的工具集里（见 agents.AgentTool.runAsTeammate）。
// Lead 掌握进度靠队员完成时回传的 team notification。
//
// TeamDelete 留着是为了收尾：队员挂在 Team 上，活干完得有办法停掉它们、
// 清理团队目录。TeamCreate 不在这里，因为 Agent 工具在指定的 Team 不存在时
// 会自己建，Lead 直接派人就行，不必先走一步建团队。
//
// 四阶段工作流：
// 1. Research: 队员并行调查，Lead 不下场
// 2. Synthesis: Lead 消化调查结果，写出实施规格
// 3. Implementation: 队员按规格改代码并提交
// 4. Verification: 队员验证改动是否正确
var CoordinatorAllowedTools = map[string]bool{
	"Agent":           true,
	"SendMessage":     true,
	"TeamDelete":      true,
	"AskUserQuestion": true,
}

// IsCoordinatorTool checks if a tool is allowed in Coordinator Mode.
func IsCoordinatorTool(name string) bool {
	return CoordinatorAllowedTools[name]
}

// CoordinatorToolFilter 返回 Lead Agent 的工具过滤函数。
// enabled 为 false 时返回 nil（不限制），为 true 时整个会话都只放行白名单内的工具。
//
// 判定只看配置，不看团队是否存在：模式在会话中途切换会留下麻烦，
// 已经发出去的调度指引留在对话历史里撤不回来，模型会照着过期的约束继续做事。
func CoordinatorToolFilter(enabled bool) func(name string) bool {
	if !enabled {
		return nil
	}
	return IsCoordinatorTool
}

// CoordinatorGuidelines 是 coordinator 模式开启时注入 Lead 每轮上下文的
// 调度指引。工具收窄和调度指引必须同时出现：只收窄工具不给指引，Lead 只会
// 发现自己读不了文件，却不知道该派队员去读。
func CoordinatorGuidelines() string {
	return `<coordinator-mode>
You are in coordinator mode. Your toolset is limited to scheduling tools
(Agent, SendMessage, TeamDelete, AskUserQuestion) — you cannot read files,
search code, or run commands yourself. This is intentional: your context
must stay reserved for task decomposition, teammate state, and message routing.

Follow the four-phase workflow:
1. Research — spawn teammates to investigate the codebase in parallel.
   They report findings back via SendMessage(to="lead").
2. Synthesis — digest the reports, write a concrete implementation spec.
3. Implementation — spawn teammates with the spec; they edit code and report back.
4. Verification — spawn a teammate to verify the changes (tests, builds).

Rules:
- Never ask "want me to proceed?" — dispatch the next phase immediately.
- Write self-contained prompts for every teammate; they have no shared context.
- Teammate results arrive as <team-notification> messages; act on them, do not wait.
- When all work is done, summarize for the user and call TeamDelete to tear down.
</coordinator-mode>`
}
