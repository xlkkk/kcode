// workflow_gate.go — 工作流门禁 Middleware。
//
// 与 Hooks/Permissions 架构对称:workflow/ 包提供纯函数与状态,
// 本 Middleware 负责在 Eino ReAct Loop 中编排调用。
//
// 钩子:
//   - WrapInvokableToolCall:     按当前阶段拦截写类工具(工具矩阵门禁)
//   - BeforeModelRewriteState:   阶段变化时注入约束消息 + 待注入审核提示
//   - AfterModelRewriteState:    检测产出物并落盘;模块文档未审核时挂审核提示

package middleware

import (
	"context"
	"encoding/json"
	"sync"

	"kcode/internal/workflow"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// WorkflowGateMW 工作流门禁中间件。state/store 为 nil 时不可用(不会注册)。
type WorkflowGateMW struct {
	*adk.BaseChatModelAgentMiddleware

	def     workflow.WorkflowDef
	state   *workflow.WorkflowState
	store   *workflow.Store
	workDir string

	mu sync.Mutex
	// lastStage 记录上次注入约束的阶段,避免每轮重复注入静态约束。
	lastStage workflow.Stage
	// pendingHint 下一轮注入的一次性提示(审核提醒等)。
	pendingHint string
}

// NewWorkflowGateMW 创建门禁中间件
func NewWorkflowGateMW(def workflow.WorkflowDef, state *workflow.WorkflowState, store *workflow.Store, workDir string) *WorkflowGateMW {
	return &WorkflowGateMW{
		def:       def,
		state:     state,
		store:     store,
		workDir:   workDir,
		lastStage: state.Stage,
	}
}

// WrapInvokableToolCall 拦截工具调用:写类工具按阶段矩阵放行/阻断。
// 阻断时返回 "Blocked by workflow: ..." 且不执行原工具,LLM 看到结果后改方向。
func (m *WorkflowGateMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		var args map[string]any
		_ = json.Unmarshal([]byte(argumentsInJSON), &args)
		path, _ := args["path"].(string)

		if ok, reason := workflow.CanUseTool(m.currentStage(), tCtx.Name, path); !ok {
			return "Blocked by workflow: " + reason, nil
		}
		return endpoint(ctx, argumentsInJSON, opts...)
	}, nil
}

// BeforeModelRewriteState 阶段变化时注入约束消息,并注入待发送的审核提示。
func (m *WorkflowGateMW) BeforeModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	stage := m.currentStage()

	m.mu.Lock()
	hint := m.pendingHint
	m.pendingHint = ""
	changed := stage != m.lastStage
	m.lastStage = stage
	m.mu.Unlock()

	if changed {
		if constraint := workflow.StageConstraint(m.state); constraint != "" {
			state.Messages = append(state.Messages, schema.SystemMessage(constraint))
		}
	}
	if hint != "" {
		state.Messages = append(state.Messages, schema.SystemMessage(hint))
	}
	return ctx, state, nil
}

// AfterModelRewriteState 检测产出物并落盘;模块文档产出未审核时挂审核提示。
func (m *WorkflowGateMW) AfterModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if workflow.DetectArtifacts(m.state, m.workDir) {
		if m.store != nil {
			_ = m.store.Save(m.state)
		}
	}
	if workflow.NeedsReview(m.def, m.state) {
		m.mu.Lock()
		m.pendingHint = "docs/modules.md 已产出但尚未通过人工审核:请立即调用 request_review 提交审核,审核通过前无法推进到 code_gen 阶段。"
		m.mu.Unlock()
	}
	return ctx, state, nil
}

// currentStage 线程安全读取当前阶段。
func (m *WorkflowGateMW) currentStage() workflow.Stage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Stage
}
