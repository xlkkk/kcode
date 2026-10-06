package skills

import "strings"

// SkillMeta Skill 元数据（YAML frontmatter）
type SkillMeta struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	WhenToUse   string   `yaml:"when_to_use"`
	Tags        []string `yaml:"tags"`
	// Mode 执行模式："inline"（默认）注入当前对话；"fork" 隔离子 Agent（CLI 暂不支持）
	Mode string `yaml:"mode"`
	// Model 覆盖 LLM（空 = 继承主循环）
	Model string `yaml:"model"`
	// Context 向后兼容：context: fork 等价 mode: fork
	Context string `yaml:"context"`
	// ForkContext 子 Agent 继承上下文策略："full"/"recent"/"none"（默认）
	ForkContext string `yaml:"fork_context"`
}

// IsFork 是否为隔离模式
func (m SkillMeta) IsFork() bool {
	return m.Mode == "fork" || m.Context == "fork"
}

// Skill 单个 Skill 定义
type Skill struct {
	Meta       SkillMeta
	PromptBody string // Markdown 正文
	SourceDir  string // 磁盘目录（内置 skill 为空）
	// IsDirectory 标记磁盘目录型 skill（有 references/ 等附属文件）
	IsDirectory bool
	// BodyLoaded 标记 PromptBody 是否已从磁盘读取
	BodyLoaded bool
}

// Render 返回替换 $ARGUMENTS 后的 skill 正文。
// 无占位符且 args 非空时，args 追加到 "## User Request" 段。
func (s *Skill) Render(args string) string {
	body := s.PromptBody
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", args)
	}
	if strings.TrimSpace(args) == "" {
		return body
	}
	return body + "\n\n## User Request\n\n" + args
}
