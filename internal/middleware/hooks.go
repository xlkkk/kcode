package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"kcode/internal/hooks"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// HooksMW — 用户 Hook 配置直接通过 Eino Middleware 驱动
//
// 不需要独立的 Engine：Middleware 本身就是钩子的执行者。
// 用户配置 []hooks.Hook → HooksMW 在每个生命周期点匹配并执行。
//
// 事件映射：
//   - BeforeAgent             → session_start
//   - AfterAgent              → session_end
//   - BeforeModelRewriteState → pre_send / turn_start（prompt 注入到 Messages）
//   - AfterModelRewriteState  → post_receive / turn_end
//   - WrapInvokableToolCall   → pre_tool_use / post_tool_use
type HooksMW struct {
	*adk.BaseChatModelAgentMiddleware

	hooks []hooks.Hook
	mu    sync.Mutex
	fired map[string]bool // once 追踪

	// pendingPrompts 缓存 prompt 类型 hook 的输出，
	// 在 BeforeModelRewriteState 时注入到 state.Messages
	pendingPrompts []*schema.Message
}

// NewHooksMW 从 hook 配置创建中间件
func NewHooksMW(hookConfigs []hooks.Hook) *HooksMW {
	return &HooksMW{
		hooks: hookConfigs,
		fired: make(map[string]bool),
	}
}

// --- 内部方法 ---

// fire 执行匹配指定事件的所有 hook，收集 prompt 类型结果
func (m *HooksMW) fire(event hooks.EventName, ctx hooks.HookContext) {
	for _, h := range m.hooks {
		if h.Event != event {
			continue
		}
		if !m.shouldFire(h, ctx) {
			continue
		}
		if h.Async {
			go func(h hooks.Hook) {
				res := hooks.ExecuteAction(h, ctx)
				if !res.Success {
					fmt.Printf("[hook] %s failed: %s\n", h.ID, res.Output)
				}
			}(h)
			continue
		}
		result := hooks.ExecuteAction(h, ctx)
		// prompt 类型且成功 → 缓存为系统消息，等待注入
		if h.Action.Type == hooks.ActionPrompt && result.Success {
			m.addPendingPrompt(result.Output)
		}
	}
}

// firePreToolUse 执行 pre_tool_use hook，返回 (rejected, message)
func (m *HooksMW) firePreToolUse(ctx hooks.HookContext) (bool, string) {
	for _, h := range m.hooks {
		if h.Event != hooks.EventPreToolUse {
			continue
		}
		if !m.shouldFire(h, ctx) {
			continue
		}
		result := hooks.ExecuteAction(h, ctx)
		// prompt 类型 → 缓存
		if h.Action.Type == hooks.ActionPrompt && result.Success {
			m.addPendingPrompt(result.Output)
		}
		// reject 判断
		if h.Reject || (!result.Success && h.OnError == "reject") {
			msg := result.Output
			if msg == "" {
				msg = "blocked by hook " + h.ID
			}
			return true, msg
		}
	}
	return false, ""
}

// addPendingPrompt 线程安全地添加待注入的 prompt 消息
func (m *HooksMW) addPendingPrompt(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingPrompts = append(m.pendingPrompts, schema.SystemMessage(content))
}

// drainPendingPrompts 取出并清空待注入的 prompt
func (m *HooksMW) drainPendingPrompts() []*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	prompts := m.pendingPrompts
	m.pendingPrompts = nil
	return prompts
}

func (m *HooksMW) shouldFire(h hooks.Hook, ctx hooks.HookContext) bool {
	if h.Condition != "" && !hooks.EvaluateCondition(h.Condition, ctx) {
		return false
	}
	if h.Once {
		m.mu.Lock()
		defer m.mu.Unlock()
		if h.ID != "" && m.fired[h.ID] {
			return false
		}
		if h.ID != "" {
			m.fired[h.ID] = true
		}
	}
	return true
}

// --- Eino Middleware 接口实现 ---

// BeforeAgent → session_start
func (m *HooksMW) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	m.fire(hooks.EventSessionStart, hooks.HookContext{EventName: hooks.EventSessionStart})
	return ctx, runCtx, nil
}

// AfterAgent → session_end
func (m *HooksMW) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	m.fire(hooks.EventSessionEnd, hooks.HookContext{EventName: hooks.EventSessionEnd})
	return ctx, nil
}

// BeforeModelRewriteState → pre_send + turn_start，注入累积的 prompt 到 Messages
func (m *HooksMW) BeforeModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	hctx := hooks.HookContext{EventName: hooks.EventPreSend}
	m.fire(hooks.EventPreSend, hctx)
	m.fire(hooks.EventTurnStart, hctx)

	// 将所有待注入的 prompt 添加到消息列表
	if prompts := m.drainPendingPrompts(); len(prompts) > 0 {
		state.Messages = append(state.Messages, prompts...)
	}

	return ctx, state, nil
}

// AfterModelRewriteState → post_receive + turn_end
func (m *HooksMW) AfterModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	hctx := hooks.HookContext{EventName: hooks.EventPostReceive}
	m.fire(hooks.EventPostReceive, hctx)
	m.fire(hooks.EventTurnEnd, hctx)

	// post 阶段产生的 prompt 也注入（下一轮生效）
	if prompts := m.drainPendingPrompts(); len(prompts) > 0 {
		state.Messages = append(state.Messages, prompts...)
	}

	return ctx, state, nil
}

// WrapInvokableToolCall → pre_tool_use + post_tool_use
func (m *HooksMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		var args map[string]any
		json.Unmarshal([]byte(argumentsInJSON), &args)
		filePath := extractFilePath(args)

		// pre_tool_use（可拦截）
		preCtx := hooks.HookContext{
			EventName: hooks.EventPreToolUse,
			ToolName:  tCtx.Name,
			ToolArgs:  args,
			FilePath:  filePath,
		}
		if rejected, msg := m.firePreToolUse(preCtx); rejected {
			return "Blocked by hook: " + msg, nil
		}

		// 执行原工具
		result, err := endpoint(ctx, argumentsInJSON, opts...)

		// post_tool_use（仅通知）
		postCtx := preCtx
		postCtx.EventName = hooks.EventPostToolUse
		if err != nil {
			postCtx.Error = err.Error()
		}
		m.fire(hooks.EventPostToolUse, postCtx)

		return result, err
	}, nil
}

// extractFilePath 从工具参数中提取文件路径
func extractFilePath(args map[string]any) string {
	for _, key := range []string{"path", "file_path"} {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}
