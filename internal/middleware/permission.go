package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"kcode/internal/permissions"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// PermissionMW — 权限检查 Middleware
//
// 直接持有 Sandbox/RuleEngine/Mode，内联 5 层权限检查逻辑。
// 与 Hooks 架构对称：permissions/ 包提供类型和纯函数，Middleware 负责编排。
//
// 钩子：WrapInvokableToolCall
// 职责：工具执行前做 5 层权限检查
//   - Layer 1: 安全命令白名单自动放行
//   - Layer 2: 危险命令黑名单直接拒绝
//   - Layer 3: 路径沙箱检查
//   - Layer 4: 规则引擎
//   - Layer 5: 权限模式（default/acceptEdits/bypass）
//   - Ask → 通过 ConfirmFunc 等待用户确认
type PermissionMW struct {
	*adk.BaseChatModelAgentMiddleware

	Sandbox    *permissions.PathSandbox
	RuleEngine *permissions.RuleEngine
	Mode       permissions.PermissionMode

	// ConfirmFunc 向用户请求确认。
	// 返回 true = 用户允许本次执行；返回 false = 用户拒绝。
	// 为 nil 时 Ask 按拒绝处理。
	ConfirmFunc func(toolName, description string) bool

	// AlwaysAllowFunc 向用户请求"始终允许"确认。
	// 返回 true 时自动写入本地规则文件。
	// 为 nil 时跳过"始终允许"选项。
	AlwaysAllowFunc func(toolName, description string) bool
}

// NewPermissionMW 创建权限检查 Middleware
func NewPermissionMW(sandbox *permissions.PathSandbox, ruleEngine *permissions.RuleEngine, mode permissions.PermissionMode) *PermissionMW {
	return &PermissionMW{
		Sandbox:    sandbox,
		RuleEngine: ruleEngine,
		Mode:       mode,
	}
}

// WrapInvokableToolCall 拦截工具调用，执行前做权限检查
func (m *PermissionMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		args := parseToolArgs(argumentsInJSON)

		// 执行 5 层权限检查
		decision := m.check(tCtx.Name, args)

		switch decision.Effect {
		case permissions.Deny:
			return fmt.Sprintf("Permission denied: %s", decision.Reason), nil

		case permissions.Ask:
			desc := describeToolAction(tCtx.Name, args)

			// 先尝试"始终允许"选项
			if m.AlwaysAllowFunc != nil && m.AlwaysAllowFunc(tCtx.Name, desc) {
				content := permissions.ExtractContent(tCtx.Name, args)
				pattern := content + "*"
				if len(content) > 60 {
					pattern = content[:60] + "*"
				}
				m.RuleEngine.AppendLocalRule(permissions.Rule{
					ToolName: tCtx.Name,
					Pattern:  pattern,
					Effect:   permissions.RuleAllow,
				})
				break
			}

			// 单次确认
			if m.ConfirmFunc == nil || !m.ConfirmFunc(tCtx.Name, desc) {
				return "Tool execution rejected by user", nil
			}

		case permissions.Allow:
			// 直接放行
		}

		return endpoint(ctx, argumentsInJSON, opts...)
	}, nil
}

// check 执行 5 层权限检查（内联，不依赖 Checker）
func (m *PermissionMW) check(toolName string, args map[string]any) permissions.Decision {
	content := permissions.ExtractContent(toolName, args)
	cat := permissions.GetToolCategory(toolName)

	// 规则快照按需取一次
	snapshot := sync.OnceValue(m.RuleEngine.Snapshot)

	// Layer 1: 安全只读命令自动放行
	if cat == permissions.CategoryCommand && permissions.IsSafeCommand(content) {
		return permissions.Decision{Effect: permissions.Allow, Reason: "Safe read-only command"}
	}

	// Layer 2: 危险命令检测（Bash 专用）
	if cat == permissions.CategoryCommand {
		if hit, reason := permissions.DetectDangerous(content); hit {
			return permissions.Decision{Effect: permissions.Deny, Reason: fmt.Sprintf("Dangerous command blocked: %s", reason)}
		}
	}

	// Layer 3: 路径沙箱检查（文件工具）
	if (cat == permissions.CategoryRead || cat == permissions.CategoryWrite) && content != "" {
		if cat == permissions.CategoryWrite {
			if ok, reason := m.Sandbox.CheckDenyWrite(content); !ok {
				return permissions.Decision{Effect: permissions.Deny, Reason: reason}
			}
		}
		if ok, reason := m.Sandbox.Check(content); !ok {
			if m.Mode != permissions.ModeBypass {
				return permissions.Decision{Effect: permissions.Ask, Reason: fmt.Sprintf("Path sandbox: %s", reason)}
			}
		}
	}

	// Layer 4: 规则引擎
	ruleResult := permissions.EvaluateRules(snapshot(), toolName, content)
	if ruleResult != nil {
		switch *ruleResult {
		case permissions.RuleAllow:
			return permissions.Decision{Effect: permissions.Allow, Reason: "Permission rule: allow"}
		case permissions.RuleAsk:
			return permissions.Decision{Effect: permissions.Ask, Reason: "Permission rule: ask"}
		default:
			return permissions.Decision{Effect: permissions.Deny, Reason: "Permission rule: deny"}
		}
	}

	// Layer 5: 权限模式
	effect := permissions.ModeDecide(m.Mode, cat)
	if effect == permissions.Allow {
		return permissions.Decision{Effect: permissions.Allow, Reason: fmt.Sprintf("Permission mode %s: allow", m.Mode)}
	}
	if effect == permissions.Deny {
		return permissions.Decision{Effect: permissions.Deny, Reason: fmt.Sprintf("Permission mode %s: deny", m.Mode)}
	}

	return permissions.Decision{Effect: permissions.Ask, Reason: "User confirmation required"}
}

// --- 辅助函数 ---

// parseToolArgs 将 JSON 字符串解析为 map
func parseToolArgs(jsonStr string) map[string]any {
	var args map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &args); err != nil {
		return nil
	}
	return args
}

// describeToolAction 为 HITL 确认生成人类可读的操作描述
func describeToolAction(toolName string, args map[string]any) string {
	content := permissions.ExtractContent(toolName, args)
	if content != "" {
		return content
	}
	var parts []string
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, s))
	}
	if len(parts) > 0 {
		return strings.Join(parts, ", ")
	}
	return toolName
}
