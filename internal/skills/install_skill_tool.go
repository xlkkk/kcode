package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var InstallSkillToolName = "InstallSkill"

// InstallSkillTool — 从 URL 下载安装 Skill
//
// 用户粘贴 skills.sh / github.com URL 时，LLM 调用此工具。
// 安装完成后刷新 Catalog，新 skill 立即可用。
type InstallSkillTool struct {
	Catalog     *Catalog
	OnInstalled func(name string) // 安装后回调（注册斜杠命令等）
	InstallRoot string            // 覆盖默认安装目录（测试用）
}

var _ tool.InvokableTool = &InstallSkillTool{}

func (t *InstallSkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: InstallSkillToolName,
		Desc: "Download and install a Skill from a URL into the user-global skills directory " +
			"(~/.kcode/skills/). Supports skills.sh URLs, GitHub tree URLs, and raw SKILL.md URLs. " +
			"After install the Skill becomes available via LoadSkill. " +
			"Call this when the user pastes a Skill URL and asks to install it.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"url": {
				Type:     schema.String,
				Desc:     "The Skill URL to fetch. Examples: \"https://www.skills.sh/anthropics/skills/frontend-design\", \"https://github.com/anthropics/skills/tree/main/skills/pdf\".",
				Required: true,
			},
		}),
	}, nil
}

func (t *InstallSkillTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.URL == "" {
		return "", fmt.Errorf("url is required")
	}

	src, err := ParseSkillURL(args.URL)
	if err != nil {
		return "", err
	}

	root := t.InstallRoot
	if root == "" {
		r, err := UserSkillsRoot()
		if err != nil {
			return "", err
		}
		root = r
	}

	report, err := Install(src, root)
	if err != nil {
		return "", fmt.Errorf("install failed: %v", err)
	}

	// 刷新 Catalog
	if t.Catalog != nil {
		t.Catalog.Reload(t.Catalog.workDir)
	}
	if t.OnInstalled != nil {
		t.OnInstalled(report.SkillName)
	}

	return fmt.Sprintf(
		"Installed skill %q from %s into %s (%d files, %d bytes). Now available — call LoadSkill({name: %q}).",
		report.SkillName, src.Original, report.TargetDir, report.FileCount, report.TotalBytes, report.SkillName,
	), nil
}

// NewInstallSkillTool 创建 InstallSkill 工具
func NewInstallSkillTool(catalog *Catalog, onInstalled func(string)) tool.BaseTool {
	return &InstallSkillTool{Catalog: catalog, OnInstalled: onInstalled}
}
