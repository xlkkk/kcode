package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseFrontmatterOnly 阶段一加载：只读 frontmatter，不读 body。
// 支持两种布局：
//   - <dir>/skill.yaml（+ 可选 prompt.md）
//   - <dir>/SKILL.md（--- YAML frontmatter）
func parseFrontmatterOnly(dir string) (*Skill, error) {
	// 优先尝试 skill.yaml
	yamlPath := filepath.Join(dir, "skill.yaml")
	if data, err := os.ReadFile(yamlPath); err == nil {
		var meta SkillMeta
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parse skill.yaml: %w", err)
		}
		applyMetaDefaults(&meta, dir, "")
		return &Skill{
			Meta:        meta,
			SourceDir:   dir,
			IsDirectory: true,
			BodyLoaded:  false,
		}, nil
	}

	// 回退到 SKILL.md
	mdPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("no skill.yaml or SKILL.md: %w", err)
	}
	meta, _ := splitFrontmatter(string(data))
	applyMetaDefaults(&meta, dir, string(data))
	return &Skill{
		Meta:        meta,
		SourceDir:   dir,
		IsDirectory: true,
		BodyLoaded:  false,
	}, nil
}

// loadSkillBody 读取 skill 正文（热重载，每次调用都重新读盘）。
// 出错时保留已有 PromptBody，返回 error 让调用方决定如何处理。
func loadSkillBody(skill *Skill) error {
	// skill.yaml 布局：正文在 prompt.md
	yamlPath := filepath.Join(skill.SourceDir, "skill.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		promptPath := filepath.Join(skill.SourceDir, "prompt.md")
		body, err := os.ReadFile(promptPath)
		if err != nil {
			return fmt.Errorf("read prompt.md: %w", err)
		}
		skill.PromptBody = string(body)
		skill.BodyLoaded = true
		return nil
	}

	// SKILL.md 布局：frontmatter 后面的 markdown
	mdPath := filepath.Join(skill.SourceDir, "SKILL.md")
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}
	_, body := splitFrontmatter(string(data))
	skill.PromptBody = body
	skill.BodyLoaded = true
	return nil
}

// loadSkillFromBytes 从内存字节解析（用于 go:embed 内置 skill）。
func loadSkillFromBytes(name string, mdBytes []byte) (*Skill, error) {
	meta, body := splitFrontmatter(string(mdBytes))
	if meta.Name == "" {
		meta.Name = name
	}
	applyMetaDefaults(&meta, name, string(mdBytes))
	return &Skill{
		Meta:        meta,
		PromptBody:  body,
		SourceDir:   "",
		IsDirectory: false,
		BodyLoaded:  true,
	}, nil
}

// splitFrontmatter 分离 YAML frontmatter 和 markdown body。
// 无 --- 时返回零值 meta。
func splitFrontmatter(content string) (SkillMeta, string) {
	var meta SkillMeta
	body := content

	if strings.HasPrefix(strings.TrimSpace(content), "---") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) >= 3 {
			if err := yaml.Unmarshal([]byte(parts[1]), &meta); err == nil {
				body = strings.TrimSpace(parts[2])
			}
		}
	}
	return meta, body
}

// applyMetaDefaults 填充缺失字段：
//   - name 空 → 从目录名推导（小写 + 连字符）
//   - description 空 → body 首个非空非标题行
//   - mode 空 → "inline"（或 context=fork 时 "fork"）
//   - fork_context 空 → "none"
func applyMetaDefaults(meta *SkillMeta, dirOrName, body string) {
	if meta.Name == "" {
		base := filepath.Base(dirOrName)
		meta.Name = strings.ToLower(strings.ReplaceAll(base, " ", "-"))
	}
	if meta.Description == "" && body != "" {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "---") {
				meta.Description = line
				break
			}
		}
	}
	if meta.Mode == "" {
		if meta.Context == "fork" {
			meta.Mode = "fork"
		} else {
			meta.Mode = "inline"
		}
	}
	if meta.IsFork() && meta.ForkContext == "" {
		meta.ForkContext = "none"
	}
}
