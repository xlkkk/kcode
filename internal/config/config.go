package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"kcode/internal/feishu"
	"kcode/internal/hooks"
	"kcode/internal/mcp"
	"kcode/internal/workflow"
)

// envKeyMap provider 协议 → 环境变量名
var envKeyMap = map[string]string{
	"anthropic":     "ANTHROPIC_API_KEY",
	"openai":        "OPENAI_API_KEY",
	"openai-compat": "OPENAI_API_KEY",
}

var validProtocols = map[string]bool{
	"anthropic":     true,
	"openai":        true,
	"openai-compat": true,
}

// ConfigError 配置错误
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string { return e.Message }

// ProviderConfig LLM 提供者配置
type ProviderConfig struct {
	Name            string `yaml:"name"`
	Protocol        string `yaml:"protocol"`
	BaseURL         string `yaml:"base_url"`
	Model           string `yaml:"model"`
	APIKey          string `yaml:"api_key"`
	Thinking        bool   `yaml:"thinking"`
	ContextWindow   int    `yaml:"context_window"`
	MaxOutputTokens int    `yaml:"max_output_tokens"`
}

// ResolveAPIKey 解析 API Key（配置优先，否则取环境变量）
func (p *ProviderConfig) ResolveAPIKey() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	envVar := envKeyMap[p.Protocol]
	if envVar == "" {
		return ""
	}
	return os.Getenv(envVar)
}

// GetContextWindow 获取上下文窗口大小
func (p *ProviderConfig) GetContextWindow() int {
	if p.ContextWindow > 0 {
		return p.ContextWindow
	}
	model := strings.ToLower(p.Model)
	switch {
	case strings.Contains(model, "1m") || strings.Contains(model, "gpt-4.1"):
		return 1000000
	case strings.Contains(model, "gpt-4o") || strings.Contains(model, "gpt-4-turbo"):
		return 128000
	case strings.Contains(model, "o1") || strings.Contains(model, "o3") || strings.Contains(model, "o4"):
		return 200000
	case strings.Contains(model, "claude"):
		return 200000
	}
	return 128000
}

// GetMaxOutputTokens 获取最大输出 token 数
func (p *ProviderConfig) GetMaxOutputTokens() int {
	if p.MaxOutputTokens > 0 {
		return p.MaxOutputTokens
	}
	if p.Thinking {
		return 64000
	}
	return 8192
}

// MCPServerConfig MCP 服务器配置(直接复用 mcp 包定义,避免重复)
type MCPServerConfig = mcp.MCPServerConfig

// FeishuConfig 飞书客户端配置(直接复用 feishu 包定义,避免重复)
type FeishuConfig = feishu.Config

// SandboxConfig OS 级沙箱配置
type SandboxConfig struct {
	Enabled        bool `yaml:"enabled"`
	AutoAllow      bool `yaml:"auto_allow"`
	NetworkEnabled bool `yaml:"network_enabled"`
}

// WorkflowConfig 游戏开发强制流程配置(设计见 game.md)。Enabled 为 true 时
// 注册 WorkflowGateMW 门禁与工作流工具;其余字段 YAML 化流程定义,
// 经 ToDef() 转成 workflow.WorkflowDef。
type WorkflowConfig struct {
	Enabled bool `yaml:"enabled"`

	// RequirePlan 为 true 时 init → flow_analysis 强制先读策划案
	// (read_feishu_doc 置位 PlanLoaded)。
	RequirePlan bool `yaml:"require_plan"`

	// ReviewStages 是需要人工审核的阶段;当前仅支持 module_design,
	// 不填默认 [module_design]。
	ReviewStages []string `yaml:"review_stages"`

	// RequireBuild/RequireTest 控制 code_gen → self_test / self_test → qa_cases
	// 是否实跑 go build / go test 校验;不填默认 true,显式 false 关闭。
	RequireBuild *bool `yaml:"require_build"`
	RequireTest  *bool `yaml:"require_test"`

	// RequireSimulation 控制 self_test → qa_cases 是否要求对局模拟达标
	// (simulate_match 置位 SimulationPassed);不填默认 true,显式 false 关闭。
	RequireSimulation *bool `yaml:"require_simulation"`
}

// ToDef 把配置转成 workflow.WorkflowDef(默认值对齐 workflow.DefaultDef)。
func (c WorkflowConfig) ToDef() workflow.WorkflowDef {
	def := workflow.DefaultDef()
	def.RequirePlan = c.RequirePlan
	if c.RequireBuild != nil {
		def.RequireBuild = *c.RequireBuild
	}
	if c.RequireTest != nil {
		def.RequireTest = *c.RequireTest
	}
	if c.RequireSimulation != nil {
		def.RequireSimulation = *c.RequireSimulation
	}
	if len(c.ReviewStages) > 0 {
		def.ReviewStages = nil
		for _, s := range c.ReviewStages {
			def.ReviewStages = append(def.ReviewStages, workflow.Stage(s))
		}
	}
	return def
}

// AppConfig 应用配置
type AppConfig struct {
	Providers      []ProviderConfig  `yaml:"providers"`
	PermissionMode string            `yaml:"permission_mode"`
	MCPServers     []MCPServerConfig `yaml:"mcp_servers"`
	Hooks          []hooks.Hook      `yaml:"hooks"`
	Sandbox        SandboxConfig     `yaml:"sandbox"`
	Workflow       WorkflowConfig    `yaml:"workflow"`
	Feishu         FeishuConfig      `yaml:"feishu"`
}

// LoadConfig 加载配置（三层合并：用户级 → 项目级 → 本地级）
func LoadConfig(path string) (*AppConfig, error) {
	if path != "" {
		cfg, err := loadSingleFile(path)
		if err != nil {
			return nil, err
		}
		if err := validateProviders(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, &ConfigError{Message: fmt.Sprintf("Failed to get working directory: %s", err)}
	}

	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".kcode", "config.yaml"),
		filepath.Join(wd, ".kcode", "config.yaml"),
		filepath.Join(wd, ".kcode", "config.local.yaml"),
	}

	var merged *AppConfig
	for _, p := range candidates {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}
		layer, err := loadSingleFile(p)
		if err != nil {
			return nil, err
		}
		if merged == nil {
			merged = layer
		} else {
			merged = mergeConfig(merged, layer)
		}
	}

	if merged == nil {
		return nil, &ConfigError{Message: "No config file found. Expected .kcode/config.yaml in project or ~/.kcode/config.yaml"}
	}

	if err := validateProviders(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

func loadSingleFile(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config %s: %w", path, err)
	}
	var cfg AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, &ConfigError{Message: fmt.Sprintf("Failed to parse config %s: %s", path, err)}
	}
	// 飞书凭证支持环境变量回退(yaml 为空时读 FEISHU_APP_ID/FEISHU_APP_SECRET)。
	cfg.Feishu.ResolveFromEnv()
	return &cfg, nil
}

func mergeConfig(base, override *AppConfig) *AppConfig {
	if override.Providers != nil {
		base.Providers = override.Providers
	}
	if override.PermissionMode != "" {
		base.PermissionMode = override.PermissionMode
	}
	if len(override.MCPServers) > 0 {
		byName := make(map[string]int)
		for i, s := range base.MCPServers {
			byName[s.Name] = i
		}
		for _, s := range override.MCPServers {
			if idx, ok := byName[s.Name]; ok {
				base.MCPServers[idx] = s
			} else {
				base.MCPServers = append(base.MCPServers, s)
				byName[s.Name] = len(base.MCPServers) - 1
			}
		}
	}
	// hooks 追加合并
	base.Hooks = append(base.Hooks, override.Hooks...)
	if override.Sandbox.Enabled {
		base.Sandbox = override.Sandbox
	}
	if override.Workflow.Enabled {
		base.Workflow = override.Workflow
	}
	if override.Feishu.Enabled {
		base.Feishu = override.Feishu
	}
	return base
}

func validateProviders(cfg *AppConfig) error {
	if len(cfg.Providers) == 0 {
		return &ConfigError{Message: "At least one provider must be configured"}
	}
	requiredFields := []string{"name", "protocol", "base_url", "model"}
	for i, p := range cfg.Providers {
		var missing []string
		values := map[string]string{
			"name":     p.Name,
			"protocol": p.Protocol,
			"base_url": p.BaseURL,
			"model":    p.Model,
		}
		for _, f := range requiredFields {
			if values[f] == "" {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			return &ConfigError{
				Message: fmt.Sprintf("Provider #%d: missing fields: %s", i+1, strings.Join(missing, ", ")),
			}
		}
		if !validProtocols[p.Protocol] {
			return &ConfigError{
				Message: fmt.Sprintf("Provider #%d: invalid protocol '%s', must be one of: anthropic, openai, openai-compat", i+1, p.Protocol),
			}
		}
	}
	return nil
}
