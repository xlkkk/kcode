// factory.go — 从 ProviderConfig 构造 Eino ChatModel。
//
// kcode 的 LLM 接入统一走 eino-ext 的 OpenAI 组件：openai / openai-compat
// 直连；anthropic 协议经 Anthropic 的 OpenAI 兼容端点
// (https://api.anthropic.com/v1) 接入，配置里 base_url 留空时自动补上。

package llm

import (
	"context"
	"fmt"
	"strings"

	"kcode/internal/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// anthropicCompatBaseURL 是 Anthropic 官方 OpenAI 兼容端点。
const anthropicCompatBaseURL = "https://api.anthropic.com/v1"

// NewChatModel 按 provider 配置创建 ChatModel。
func NewChatModel(ctx context.Context, p *config.ProviderConfig) (model.BaseModel[*schema.Message], error) {
	apiKey := p.ResolveAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("provider %q: no API key (set api_key in config or the %s env var)",
			p.Name, keyEnvVar(p.Protocol))
	}

	baseURL := p.BaseURL
	if p.Protocol == "anthropic" && (baseURL == "" || strings.Contains(baseURL, "api.anthropic.com")) {
		// OpenAI SDK 会再拼一层 /chat/completions，这里要去掉 v1 之外的尾巴
		baseURL = anthropicCompatBaseURL
	}

	cfg := &openai.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   p.Model,
	}
	if max := p.GetMaxOutputTokens(); max > 0 {
		cfg.MaxCompletionTokens = &max
	}

	cm, err := openai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("provider %q: create chat model: %w", p.Name, err)
	}
	return cm, nil
}

func keyEnvVar(protocol string) string {
	switch protocol {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	default:
		return "OPENAI_API_KEY"
	}
}
