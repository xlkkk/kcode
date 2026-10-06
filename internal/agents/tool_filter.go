// tool_filter.go — 子 Agent 工具池过滤。
//
// 备注 internal/agents/tool_filter.go
// 差异：kcode Registry 里没有工具名，需通过 Info(ctx) 取；工具名统一小写
// 蛇形；没有 ExitPlanMode/EnterPlanMode（不实现 plan）；没有 mcp__ 前缀约定，
// MCP 工具按注册名透传。

package agents

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// AllAgentDisallowedTools 是任何（非 fork）子 Agent 都拿不到的工具，
// Agent 定义里的白名单也开不了它们：防递归 spawn、防无 UI 环境下的 HITL 挂起。
var AllAgentDisallowedTools = map[string]bool{
	"Agent":           true,
	"AskUserQuestion": true,
	"TaskStop":        true,
	"Workflow":        true,
}

// AsyncAgentAllowedTools 是异步（后台）子 Agent 能用的工具白名单 —
// 不含 Agent（禁止嵌套 spawn）。
var AsyncAgentAllowedTools = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
	"glob":       true,
	"grep":       true,
	"bash":       true,
	"LoadSkill":  true,
	"ToolSearch": true,
}

// TeammateDisallowedTools 队友在协作工具之外额外被挡掉的工具。组建和
// 解散团队由 Lead 负责，队友只管干活和相互协调。
var TeammateDisallowedTools = []string{"TeamCreate", "TeamDelete"}

// FilterToolsForAgent 按层过滤工具（顺序）：
//  1. AllAgentDisallowedTools — 全局黑名单（fork 路径不走这里，见 cloneToolsForFork）
//  2. isAsync 时收敛到 AsyncAgentAllowedTools 白名单；isInProcessTeammate 额外放行协作工具
//  3. spec.DisallowedTools — 定义级黑名单
//  4. spec.Tools — 定义级白名单交集（"*" 表示跳过本层）
//
// allowedTools / disallowedTools 用 kcode 工具名（小写蛇形）。
func FilterToolsForAgent(ctx context.Context, all []tool.BaseTool, allowedTools, disallowedTools []string, isAsync, isInProcessTeammate bool) []tool.BaseTool {
	disallowed := make(map[string]bool, len(disallowedTools))
	for _, name := range disallowedTools {
		disallowed[name] = true
	}

	allowed := make(map[string]bool, len(allowedTools))
	hasWhitelist := len(allowedTools) > 0 && !(len(allowedTools) == 1 && allowedTools[0] == "*")
	for _, name := range allowedTools {
		allowed[name] = true
	}

	out := make([]tool.BaseTool, 0, len(all))
	for _, t := range all {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}
		name := info.Name

		// Layer 1: 全局黑名单。
		if AllAgentDisallowedTools[name] {
			continue
		}

		// Layer 2: 异步白名单（进程内队友额外放行 Agent 与协作工具）。
		if isAsync && !AsyncAgentAllowedTools[name] {
			if !(isInProcessTeammate && (name == "Agent" || TeammateCoordinationTools[name])) {
				continue
			}
		}

		// Layer 3: 定义级黑名单。
		if disallowed[name] {
			continue
		}

		// Layer 4: 定义级白名单交集。
		if hasWhitelist && !allowed[name] {
			continue
		}

		out = append(out, t)
	}
	return out
}

// TeammateCoordinationTools 队友专属的协作工具（teams 包注册后自动生效，
// 名字对不上时过滤层直接跳过，不影响其他工具）。
var TeammateCoordinationTools = map[string]bool{
	"SendMessage": true,
	"TaskCreate":  true,
	"TaskGet":     true,
	"TaskList":    true,
	"TaskUpdate":  true,
}

// cloneToolsForFork 原样复制父工具池，只把 Agent 工具换成一份
// QuerySource=ForkQuerySource 的浅拷贝 —— fork 子 Agent 再想 fork 时会在
// 调用那一刻被拒。工具定义与父 Agent 逐字节一致，prompt 缓存才能命中。
func cloneToolsForFork(all []tool.BaseTool, agentTool *AgentTool) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(all))
	for _, t := range all {
		if at, ok := t.(*AgentTool); ok && at == agentTool {
			clone := *at
			clone.QuerySource = ForkQuerySource
			out = append(out, &clone)
			continue
		}
		out = append(out, t)
	}
	return out
}
