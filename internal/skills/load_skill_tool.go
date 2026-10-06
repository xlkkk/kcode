package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var LoadSkillToolName = "LoadSkill"

// LoadSkillTool — 按需加载 Skill
//
// LLM 看到 System Prompt 的 "## Available Skills" 清单后，
// 匹配用户需求时调用此工具加载完整 body。
type LoadSkillTool struct {
	Catalog *Catalog
	Host    SkillHost
}

var _ tool.InvokableTool = &LoadSkillTool{}

func (t *LoadSkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: LoadSkillToolName,
		Desc: "Activate a Skill by name. Returns the full SOP body so you can follow its " +
			"instructions. Call this when the user's request matches one of the available " +
			"Skills listed in the available-skills section. Pass the Skill name without a leading slash.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Type:     schema.String,
				Desc:     "The Skill name to activate (e.g. \"commit\", \"review\").",
				Required: true,
			},
		}),
	}, nil
}

func (t *LoadSkillTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if t.Catalog == nil || t.Host == nil {
		return "", fmt.Errorf("LoadSkill not wired (Catalog or Host nil)")
	}

	skill, err := t.Catalog.GetFull(args.Name)
	if err != nil && skill == nil {
		return "", fmt.Errorf("unknown skill: %s", args.Name)
	}
	if skill.PromptBody == "" {
		return "", fmt.Errorf("skill %q has empty body", args.Name)
	}

	// fork 模式 CLI 暂不支持，回退到 inline
	if skill.Meta.IsFork() {
		return fmt.Sprintf("skill %q requires fork mode (not supported in CLI), running inline instead", args.Name), nil
	}

	t.Host.ActivateSkill(skill.Meta.Name, skill.PromptBody)

	header := fmt.Sprintf("# Skill: %s\n\n", skill.Meta.Name)
	return header + skill.PromptBody, nil
}

// NewLoadSkillTool 创建 LoadSkill 工具
func NewLoadSkillTool(catalog *Catalog, host SkillHost) tool.BaseTool {
	return &LoadSkillTool{Catalog: catalog, Host: host}
}
