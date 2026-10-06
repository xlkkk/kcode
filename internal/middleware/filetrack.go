package middleware

import (
	"context"

	"kcode/internal/filehistory"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// FileTrackMW — 文件变更追踪 Middleware
//
// 钩子：WrapInvokableToolCall
// 职责：在 write_file / edit_file 执行前自动调用 History.TrackEdit 备份文件，
// 使每轮对话可通过 History.Rewind 回滚到任意检查点。
//
// 备注 tools/write_file.go、tools/edit_file.go 中的
// t.FileHistory.TrackEdit(path) 内联调用；kcode 改为中间件统一拦截，
// 工具本身不感知 FileHistory。
type FileTrackMW struct {
	*adk.BaseChatModelAgentMiddleware

	History *filehistory.History
}

// NewFileTrackMW 创建文件追踪 Middleware。history 为 nil 时中间件透传。
func NewFileTrackMW(history *filehistory.History) *FileTrackMW {
	return &FileTrackMW{History: history}
}

// fileTrackTools 需要追踪的工具名 → 路径参数名
var fileTrackTools = map[string]string{
	"write_file": "path",
	"edit_file":  "path",
}

// WrapInvokableToolCall 拦截写类工具，执行前备份目标文件
func (m *FileTrackMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if m.History == nil {
		return endpoint, nil
	}

	argKey, tracked := fileTrackTools[tCtx.Name]
	if !tracked {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		args := parseToolArgs(argumentsInJSON)
		if path, ok := args[argKey].(string); ok && path != "" {
			m.History.TrackEdit(path)
		}
		return endpoint(ctx, argumentsInJSON, opts...)
	}, nil
}
