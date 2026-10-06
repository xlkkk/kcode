package tools

import (
	"github.com/cloudwego/eino/components/tool"
)

// Registry 工具注册表
//
// 管理所有已注册工具，提供 schema 生成、分类查询、延迟加载等功能。
// 每个工具实现 Eino 的 tool.InvokableTool 接口。
//
// 工具分类（Category）：
//   - read    → 可并发执行（ReadFile, Glob, Grep）
//   - write   → 串行执行（WriteFile, EditFile）
//   - command → 串行执行（Bash）
type Registry struct {
	tools []tool.BaseTool
}

// NewRegistry 创建工具注册表并注册所有默认工具
func NewRegistry() *Registry {
	r := &Registry{tools: make([]tool.BaseTool, 0)}
	// 注册默认工具
	r.Register(NewReadFileTool())
	r.Register(NewBashTool())
	r.Register(NewEditFileTool())
	r.Register(NewWriteFileTool())
	r.Register(NewGrepTool())
	r.Register(NewGlobTool())
	// r.Register(NewToolSearchTool()) // 注册工具搜索工具
	return r
}

// Register 注册一个工具
func (r *Registry) Register(t tool.BaseTool) {
	r.tools = append(r.tools, t)
}

// GetAll 获取所有已注册的工具
func (r *Registry) GetAll() []tool.BaseTool {
	return r.tools
}
