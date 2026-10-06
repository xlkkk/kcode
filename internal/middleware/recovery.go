package middleware

import (
	"context"
	"encoding/json"

	"kcode/internal/compact"
	"kcode/internal/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// RecoveryMW — 恢复状态 Middleware
//
// 钩子：WrapInvokableToolCall
// 职责：ReadFile 成功时记录文件快照到 RecoveryState
//   - 压缩后可用快照恢复文件内容（模型不必重新读文件）
//   - 压缩后附加 RecoveryAttachment（文件快照 + Skill 快照 + 工具列表）
//
// 备注 agent.go executeSingleTool() 中的 RecoveryState.RecordFileRead()
type RecoveryMW struct {
	*adk.BaseChatModelAgentMiddleware
	state *compact.RecoveryState // 自定义状态
}

// NewRecoveryMW 创建恢复状态 Middleware
func NewRecoveryMW(state *compact.RecoveryState) *RecoveryMW {
	return &RecoveryMW{
		state: state,
	}
}

// WrapInvokableToolCall 拦截工具调用，ReadFile 成功时记录文件快照
// WrapInvokableToolCall 工具执行前后
func (m *RecoveryMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint, // 真正的工具执行函数
	tCtx *adk.ToolContext, // 工具调用上下文
) (adk.InvokableToolCallEndpoint, error) {
	// 工具调用前：拦截非 read_file 工具
	if tCtx.Name != tools.ReadFileToolName {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		// 执行原工具
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return result, err
		}

		// 解析参数获取文件路径
		var args struct {
			Path string `json:"path"`
		}
		// 记录在 compact.RecoveryState 中
		if jsonErr := json.Unmarshal([]byte(argumentsInJSON), &args); jsonErr == nil && args.Path != "" {
			m.state.RecordFileRead(args.Path, result)
		}

		return result, nil
	}, nil
}
