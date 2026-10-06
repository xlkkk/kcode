package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"kcode/internal/permissions"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// SubAgentMW — 后台记忆 Agent（提取与整理）的权限保护中间件。
//
// 备注 memory.NewSubAgentChecker（permissions.Checker 版本）。
// kcode 去掉了 Checker 中间层，这里以 Eino Middleware 形式实现同等约束：
//   - write_file / edit_file：目标路径必须落在 projectMemDir 或 userMemDir 内
//   - bash：仅放行只读安全命令（permissions.IsSafeCommand）
//   - 其余读类工具（read_file / grep / glob）：不限制
type SubAgentMW struct {
	*adk.BaseChatModelAgentMiddleware

	mu            sync.Mutex
	projectMemDir string   // 带尾分隔符
	userMemDir    string   // 可为 ""
	writtenPaths  []string // 通过检查的写目标，供调用方统计
}

// NewSubAgentMW 创建后台记忆 Agent 的保护中间件。
func NewSubAgentMW(projectMemDir, userMemDir string) *SubAgentMW {
	return &SubAgentMW{
		projectMemDir: filepath.Clean(projectMemDir) + string(filepath.Separator),
		userMemDir: func() string {
			if userMemDir == "" {
				return ""
			}
			return filepath.Clean(userMemDir) + string(filepath.Separator)
		}(),
	}
}

// WrittenPaths 返回本次运行中通过沙箱检查的写入目标路径（去重，首次出现优先）。
func (m *SubAgentMW) WrittenPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.writtenPaths))
	copy(out, m.writtenPaths)
	return out
}

// allowedWriteTools 受限写工具 → 路径参数名
var subAgentWriteTools = map[string]string{
	"write_file": "path",
	"edit_file":  "path",
}

// WrapInvokableToolCall 对写类工具和 bash 做前置拦截
func (m *SubAgentMW) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	argKey, isWrite := subAgentWriteTools[tCtx.Name]
	isBash := tCtx.Name == "bash"

	if !isWrite && !isBash {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		var args map[string]any
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return endpoint(ctx, argumentsInJSON, opts...)
		}

		if isBash {
			cmd, _ := args["command"].(string)
			if !permissions.IsSafeCommand(cmd) {
				return "Permission denied: background memory agent may only run read-only commands", nil
			}
			return endpoint(ctx, argumentsInJSON, opts...)
		}

		path, _ := args[argKey].(string)
		if !m.inMemoryDirs(path) {
			return fmt.Sprintf("Permission denied: memory agent may only write inside %s or %s", m.projectMemDir, m.userMemDir), nil
		}
		m.mu.Lock()
		seen := false
		for _, p := range m.writtenPaths {
			if p == path {
				seen = true
				break
			}
		}
		if !seen {
			m.writtenPaths = append(m.writtenPaths, path)
		}
		m.mu.Unlock()
		return endpoint(ctx, argumentsInJSON, opts...)
	}, nil
}

// inMemoryDirs 判断路径（相对或绝对）是否落在允许的记忆目录内
func (m *SubAgentMW) inMemoryDirs(path string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs) + string(filepath.Separator)
	if strings.HasPrefix(abs, m.projectMemDir) {
		return true
	}
	if m.userMemDir != "" && strings.HasPrefix(abs, m.userMemDir) {
		return true
	}
	return false
}
