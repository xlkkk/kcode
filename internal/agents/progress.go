// progress.go — 子 Agent 工具调用进度中间件。
//
// 备注 agent_tool.go 里对 ToolResultEvent 的翻译逻辑。
// Eino 版直接 WrapInvokableToolCall：计时、记录成败、向 ProgressCh 发射
// SubAgentProgress，UI 层因此能看到子 Agent 每一步在用什么工具。

package agents

import (
	"context"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

type subProgressMW struct {
	*adk.BaseChatModelAgentMiddleware

	ch        chan<- SubAgentProgress
	agentDesc string
	agentType string
}

func newSubProgressMW(ch chan<- SubAgentProgress, agentDesc, agentType string) adk.ChatModelAgentMiddleware {
	if ch == nil {
		// 无消费方时返回一个什么都不做的占位，省掉各处 nil 判断。
		return &adk.BaseChatModelAgentMiddleware{}
	}
	return &subProgressMW{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		ch:                           ch,
		agentDesc:                    agentDesc,
		agentType:                    agentType,
	}
}

func (m *subProgressMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		start := time.Now()
		out, err := endpoint(ctx, argumentsInJSON, opts...)
		emitProgress(m.ch, ctx, SubAgentProgress{
			AgentDesc: m.agentDesc,
			AgentType: m.agentType,
			ToolName:  tCtx.Name,
			Elapsed:   time.Since(start).Seconds(),
			IsError:   err != nil,
		})
		return out, err
	}, nil
}
