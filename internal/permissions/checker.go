package permissions

// 权限类型定义 — 纯数据和纯函数
//
// PermissionMW（Middleware）负责编排这些类型完成 5 层权限检查。
// 架构与 hooks/ 包对称：本包提供类型 + 纯函数，Middleware 负责调用。

// DecisionEffect 裁决效果
type DecisionEffect string

const (
	Allow DecisionEffect = "allow"
	Deny  DecisionEffect = "deny"
	Ask   DecisionEffect = "ask"
)

// Decision 权限裁决结果
type Decision struct {
	Effect DecisionEffect
	Reason string
}

// PermissionMode 权限模式
type PermissionMode string

const (
	ModeDefault     PermissionMode = "default"           // read=allow, write=ask, command=ask
	ModeAcceptEdits PermissionMode = "acceptEdits"       // read=allow, write=allow, command=ask
	ModeBypass      PermissionMode = "bypassPermissions" // all=allow
)

// ToolCategory 工具分类
type ToolCategory string

const (
	CategoryRead    ToolCategory = "read"
	CategoryWrite   ToolCategory = "write"
	CategoryCommand ToolCategory = "command"
)

// modeMatrix 权限模式矩阵
var modeMatrix = map[PermissionMode]map[ToolCategory]DecisionEffect{
	ModeDefault: {
		CategoryRead:    Allow,
		CategoryWrite:   Ask,
		CategoryCommand: Ask,
	},
	ModeAcceptEdits: {
		CategoryRead:    Allow,
		CategoryWrite:   Allow,
		CategoryCommand: Ask,
	},
	ModeBypass: {
		CategoryRead:    Allow,
		CategoryWrite:   Allow,
		CategoryCommand: Allow,
	},
}

// ModeDecide 根据权限模式和工具分类返回裁决
func ModeDecide(mode PermissionMode, category ToolCategory) DecisionEffect {
	m, ok := modeMatrix[mode]
	if !ok {
		return Ask
	}
	return m[category]
}

// toolCategoryMap 工具名到分类的映射
var toolCategoryMap = map[string]ToolCategory{
	"read_file":  CategoryRead,
	"glob":       CategoryRead,
	"grep":       CategoryRead,
	"write_file": CategoryWrite,
	"edit_file":  CategoryWrite,
	"bash":       CategoryCommand,
}

// GetToolCategory 获取工具分类
func GetToolCategory(toolName string) ToolCategory {
	if cat, ok := toolCategoryMap[toolName]; ok {
		return cat
	}
	// MCP 工具默认为 command（需要确认）
	return CategoryCommand
}

// contentFields 工具名到内容字段的映射
var contentFields = map[string]string{
	"bash":       "command",
	"read_file":  "path",
	"write_file": "path",
	"edit_file":  "path",
	"glob":       "pattern",
	"grep":       "pattern",
}

// ExtractContent 从工具参数中提取用于规则匹配的内容
func ExtractContent(toolName string, args map[string]any) string {
	field, ok := contentFields[toolName]
	if !ok {
		return ""
	}
	v, _ := args[field].(string)
	return v
}
