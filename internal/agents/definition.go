package agents

// definition.go — Agent 定义类型（YAML frontmatter + markdown body）。
//
// 备注 internal/agents/definition.go
// 差异：permissionMode 去掉 "plan"（kcode 不实现 plan 模式）；
// Code.md 相关字段改名 KCODE.md。

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentMemoryScope 持久记忆位置：per-user、per-project 或 per-checkout（不进版本控制）。
type AgentMemoryScope string

const (
	AgentMemoryScopeUser    AgentMemoryScope = "user"
	AgentMemoryScopeProject AgentMemoryScope = "project"
	AgentMemoryScopeLocal   AgentMemoryScope = "local"
)

// IsolationMode 对应 frontmatter 里的 isolation 字段。
type IsolationMode string

const (
	IsolationWorktree IsolationMode = "worktree"
	IsolationRemote   IsolationMode = "remote"
)

// AgentDefinition 部分字段（Effort、Skills、McpServers、Hooks、Memory、
// InitialPrompt、OmitKcodeMd、RequiredMcpServers）暂无运行时用途，但仍然解析并
// 保留，避免用户定义在往返中丢数据，将来接入时也不必再做一次 schema 迁移。
type AgentDefinition struct {
	AgentType       string   `yaml:"name"`
	WhenToUse       string   `yaml:"description"`
	Tools           []string `yaml:"tools"`
	DisallowedTools []string `yaml:"disallowedTools"`
	Model           string   `yaml:"model"`
	MaxTurns        int      `yaml:"maxTurns"`

	// PermissionMode 覆盖父 Agent 的权限模式。合法值见 internal/permissions.PermissionMode。
	PermissionMode string `yaml:"permissionMode"`

	// Effort 是给模型的任务复杂度提示（"low" | "medium" | "high" | int）。目前只存不用。
	Effort any `yaml:"effort"`

	// Skills 是子 Agent 启动时预加载的 skill 名。
	Skills []string `yaml:"skills"`

	// McpServers 是作用于该 agent 的 MCP server 名或内联配置，存原始 any，
	// 将来加载层可同时解释字符串引用和内联配置。
	McpServers []any `yaml:"mcpServers"`

	// RequiredMcpServers 是准入门槛：列出的 server 加载时不可用则该 agent 被过滤掉。
	RequiredMcpServers []string `yaml:"requiredMcpServers"`

	// Hooks 是该 agent 启动时注册的会话级 hooks，存原始 YAML，消费时再类型检查。
	Hooks any `yaml:"hooks"`

	// Memory 三种作用域的持久记忆开关。
	Memory AgentMemoryScope `yaml:"memory"`

	// Background 为真时该 agent 无论调用方传什么都以后台任务运行。
	Background bool `yaml:"background"`

	// Isolation 选择文件系统隔离模式。
	Isolation IsolationMode `yaml:"isolation"`

	// InitialPrompt 拼在第一个 user turn 前面。
	InitialPrompt string `yaml:"initialPrompt"`

	// OmitKcodeMd 从该 agent 的 user context 中省略 KCODE.md 层级。
	// 只读 agent（explore）靠它省 token。
	OmitKcodeMd bool `yaml:"omitKcodeMd"`

	// SystemPrompt 是定义文件的 markdown body。
	SystemPrompt string `yaml:"-"`

	// FilePath / Source / Filename 在加载时填充。
	FilePath string `yaml:"-"`
	Source   string `yaml:"-"`
	Filename string `yaml:"-"`
}

// validPermissionModes 是 Agent 定义里 permissionMode 字段的合法取值，空串表示
// 不覆盖、沿用父 Agent 的模式。kcode 没有 plan 模式。
var validPermissionModes = map[string]bool{
	"":                  true,
	"default":           true,
	"acceptEdits":       true,
	"bypassPermissions": true,
}

var validMemoryScopes = map[AgentMemoryScope]bool{
	"":                      true,
	AgentMemoryScopeUser:    true,
	AgentMemoryScopeProject: true,
	AgentMemoryScopeLocal:   true,
}

var validIsolationModes = map[IsolationMode]bool{
	"":                true,
	IsolationWorktree: true,
	IsolationRemote:   true,
}

// ParseAgentFile 解析一个 agent 定义文件（YAML frontmatter + markdown body）。
func ParseAgentFile(path string) (*AgentDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	var def AgentDefinition
	def.FilePath = path

	if strings.HasPrefix(strings.TrimSpace(content), "---") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) >= 3 {
			if err := yaml.Unmarshal([]byte(parts[1]), &def); err != nil {
				return nil, fmt.Errorf("parse frontmatter in %s: %w", path, err)
			}
			def.SystemPrompt = strings.TrimSpace(parts[2])
		}
	} else {
		def.SystemPrompt = strings.TrimSpace(content)
	}

	if def.AgentType == "" {
		return nil, fmt.Errorf("agent definition %s: missing required field 'name'", path)
	}
	if def.WhenToUse == "" {
		return nil, fmt.Errorf("agent definition %s: missing required field 'description'", path)
	}

	// model 只做规范化：小写 "inherit" 归一成哨兵值 "inherit"（表示沿用父 client），
	// 其余原样保留，交给 ModelResolver / LLM 路由去匹配，第三方模型名要能原样往返。
	def.Model = strings.TrimSpace(def.Model)
	if strings.EqualFold(def.Model, "inherit") {
		def.Model = "inherit"
	}

	if !validPermissionModes[def.PermissionMode] {
		return nil, fmt.Errorf("agent definition %s: invalid permissionMode '%s'", path, def.PermissionMode)
	}

	if !validMemoryScopes[def.Memory] {
		return nil, fmt.Errorf("agent definition %s: invalid memory scope '%s'", path, def.Memory)
	}

	if !validIsolationModes[def.Isolation] {
		return nil, fmt.Errorf("agent definition %s: invalid isolation mode '%s'", path, def.Isolation)
	}

	return &def, nil
}

// ToSpec 把加载层的定义转换成执行层使用的运行时规格。
func (d *AgentDefinition) ToSpec() SubAgentSpec {
	return SubAgentSpec{
		Name:                 d.AgentType,
		Description:          d.WhenToUse,
		Tools:                d.Tools,
		DisallowedTools:      d.DisallowedTools,
		SystemPromptOverride: d.SystemPrompt,
		MaxTurns:             d.MaxTurns,
		Model:                d.Model,
		PermissionMode:       d.PermissionMode,
		Background:           d.Background,
		Isolation:            d.Isolation,
		InitialPrompt:        d.InitialPrompt,
		OmitKcodeMd:          d.OmitKcodeMd,
		Skills:               d.Skills,
		Memory:               d.Memory,
		McpServers:           d.McpServers,
		RequiredMcpServers:   d.RequiredMcpServers,
		Hooks:                d.Hooks,
		Effort:               d.Effort,
	}
}

// HasRequiredMcpServers 在没有 MCP 要求、或每个必需 pattern 都能匹配到可用
// server 名（大小写不敏感子串）时返回 true。
func (d *AgentDefinition) HasRequiredMcpServers(availableServers []string) bool {
	if len(d.RequiredMcpServers) == 0 {
		return true
	}
	for _, pattern := range d.RequiredMcpServers {
		patLower := strings.ToLower(pattern)
		matched := false
		for _, server := range availableServers {
			if strings.Contains(strings.ToLower(server), patLower) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// SubAgentSpec 是 AgentDefinition 中运行时相关字段的子集，是加载层
// （AgentDefinition）与执行层（runSync / runAsync / runFork）之间的桥梁。
// 内置 Agent 不走文件解析，直接通过 BuiltinSpecs 实例化。
type SubAgentSpec struct {
	Name                 string
	Description          string
	Tools                []string
	DisallowedTools      []string
	SystemPromptOverride string
	MaxTurns             int
	Model                string

	// PermissionMode 覆盖父 Agent 的权限模式，空串表示继承。
	PermissionMode string

	// Background 为真时强制后台运行，无视调用方的 run_in_background。
	Background bool

	// Isolation 选择文件系统隔离模式；"worktree" 会创建临时 git worktree。
	Isolation IsolationMode

	// InitialPrompt 拼在第一个 user turn 前面。
	InitialPrompt string

	// OmitKcodeMd 从该 agent 的 user context 中省略 KCODE.md 层级。
	OmitKcodeMd bool

	// Skills 是启动时预加载的 skill 名。
	Skills []string

	// Memory 三种作用域的持久记忆开关。
	Memory AgentMemoryScope

	// McpServers / RequiredMcpServers / Hooks / Effort 把 frontmatter 数据带到
	// 运行时，供将来的通道消费而无需再做一次 schema 迁移。
	McpServers         []any
	RequiredMcpServers []string
	Hooks              any
	Effort             any
}
