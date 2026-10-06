package prompt

import (
	"fmt"
	"path/filepath"
	"strings"

	"kcode/internal/skills"
)

// BuildSkillSection 生成 "## Available Skills" 段落。
//
// 注入到 System Prompt 或首条 system-reminder，让 LLM 知道有哪些 skill 可用。
// catalog 为空时返回空字符串。
func BuildSkillSection(catalog *skills.Catalog, workDir string) string {
	if catalog == nil {
		return ""
	}
	metas := catalog.List()
	if len(metas) == 0 {
		return ""
	}

	skillsDir := filepath.Join(workDir, ".kcode", "skills")
	var sb strings.Builder

	sb.WriteString("## Available Skills\n\n")
	sb.WriteString(fmt.Sprintf("Skills are installed at: %s\n", skillsDir))
	sb.WriteString("When creating new skills, always place them under this directory as <skill-name>/SKILL.md.\n\n")
	sb.WriteString("Only Skill names and one-line descriptions are listed below. ")
	sb.WriteString("To activate a Skill on demand call the LoadSkill tool with {name: \"<skill-name>\"}. ")
	sb.WriteString("After activation the Skill's full SOP gets pinned to the environment context, and any tools the Skill declares get registered. ")
	sb.WriteString("Users can also invoke a Skill directly with /<name>.\n\n")
	sb.WriteString("If the user pastes a Skill URL (skills.sh, github.com tree URL, or raw SKILL.md URL) and asks to install / add / get it, ")
	sb.WriteString("call the InstallSkill tool with {url: \"<url>\"} — the new Skill becomes available immediately afterwards.\n\n")

	for _, meta := range metas {
		desc := meta.Description
		if len(desc) > 200 {
			desc = desc[:200] + "…"
		}
		sb.WriteString(fmt.Sprintf("- /%s: %s\n", meta.Name, desc))
	}

	return sb.String()
}
