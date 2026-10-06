// statebridge.go — 父对话消息到 AgentTool 的同步桥。
//
// Eino 的 tool 执行上下文拿不到 ChatModelAgent 的 State，fork 路径需要父
// Agent 的完整消息历史。ParentStateMW 注册在主 Agent 的 Handlers 链上，
// 每次调模型前把 state.Messages 快照进 AgentTool，runFork 直接用。

package agents

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ParentStateMW 把主 Agent 的消息快照同步给 AgentTool（fork 路径消费）。
type ParentStateMW struct {
	*adk.BaseChatModelAgentMiddleware

	tool *AgentTool
}

// NewParentStateMW 创建桥中间件。
func NewParentStateMW(tool *AgentTool) *ParentStateMW {
	return &ParentStateMW{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		tool:                         tool,
	}
}

// BeforeModelRewriteState 在每轮模型调用前同步消息快照。
func (m *ParentStateMW) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	msgs := make([]*schema.Message, len(state.Messages))
	copy(msgs, state.Messages)
	m.tool.SetParentMessages(msgs)
	return ctx, state, nil
}
