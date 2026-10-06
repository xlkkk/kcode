// feishu.go — 飞书 OpenAPI 客户端(client 模式)。
//
// 直接以 REST 方式访问飞书开放平台,不引入第三方 SDK,避免额外依赖:
//   - tenant_access_token 内部应用鉴权(缓存到过期前 1 分钟)
//   - ReadDocRaw:   读取云文档纯文本(docx raw_content)
//   - CreateDoc:    新建云文档并写入内容(文本块),返回文档 URL
//
// 用途(game.md 链路):ReadDocRaw 供 read_feishu_doc 读取策划案;
// CreateDoc 供 write_feishu_doc 把 docs/qa_cases.md 写回飞书。
//
// 文档 token 可从 URL 解析:https://xxx.feishu.cn/docx/{token}。

package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL 飞书开放平台默认域名(私有化部署可通过 config 覆盖)。
const DefaultBaseURL = "https://open.feishu.cn"

// Config 飞书客户端配置(config.yaml feishu 段)。
type Config struct {
	Enabled   bool   `yaml:"enabled"`
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	BaseURL   string `yaml:"base_url"` // 可选,默认 DefaultBaseURL
}

// ResolveFromEnv 凭证为空时从环境变量回退(config 优先,与 ProviderConfig.
// ResolveAPIKey 同一模式):FEISHU_APP_ID / FEISHU_APP_SECRET。
func (c *Config) ResolveFromEnv() {
	if c.AppID == "" {
		c.AppID = os.Getenv("FEISHU_APP_ID")
	}
	if c.AppSecret == "" {
		c.AppSecret = os.Getenv("FEISHU_APP_SECRET")
	}
}

// Client 飞书 OpenAPI 客户端。
type Client struct {
	appID     string
	appSecret string
	baseURL   string
	http      *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewClient 创建客户端。appID/appSecret 为空时返回 nil 由调用方处理
// (不启用飞书能力)。
func NewClient(cfg Config) *Client {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		appID:     cfg.AppID,
		appSecret: cfg.AppSecret,
		baseURL:   base,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// --- 公开方法 ---

// ReadDocRaw 读取云文档纯文本内容。ref 可以是文档 URL 或裸 token。
func (c *Client) ReadDocRaw(ctx context.Context, ref string) (string, error) {
	token := extractDocToken(ref)
	if token == "" {
		return "", fmt.Errorf("无法从 %q 解析飞书文档 token(需为文档 URL 或 token)", ref)
	}

	api := fmt.Sprintf("%s/open-apis/docx/v1/documents/%s/raw_content", c.baseURL, token)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, api, nil, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("飞书读取文档失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.Data.Content) == "" {
		return "", fmt.Errorf("飞书文档 %s 内容为空", token)
	}
	return resp.Data.Content, nil
}

// CreateDoc 新建飞书云文档并写入 content(按行拆成文本块),返回文档 URL。
// folderToken 可选,指定目标文件夹(目录 token,非 URL)。
func (c *Client) CreateDoc(ctx context.Context, title, content, folderToken string) (string, error) {
	// 1. 创建空文档。
	createBody, _ := json.Marshal(map[string]any{"title": title, "folder_token": folderToken})
	var created struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Document struct {
				DocumentID string `json:"document_id"`
			} `json:"document"`
		} `json:"data"`
	}
	api := c.baseURL + "/open-apis/docx/v1/documents"
	if err := c.doJSON(ctx, http.MethodPost, api, createBody, &created); err != nil {
		return "", err
	}
	if created.Code != 0 {
		return "", fmt.Errorf("飞书创建文档失败: code=%d msg=%s", created.Code, created.Msg)
	}
	docID := created.Data.Document.DocumentID
	if docID == "" {
		return "", fmt.Errorf("飞书创建文档失败:响应缺少 document_id")
	}

	// 2. 按行拆块写入根节点 children。行过长时切段,分批 ≤ blockBatch 个。
	lines := splitContent(content)
	for start := 0; start < len(lines); start += blockBatch {
		end := start + blockBatch
		if end > len(lines) {
			end = len(lines)
		}
		blocks := make([]map[string]any, 0, end-start)
		for _, line := range lines[start:end] {
			blocks = append(blocks, textBlock(line))
		}
		body, _ := json.Marshal(map[string]any{
			"children": blocks,
			"index":    start,
		})
		var appended struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		api := fmt.Sprintf("%s/open-apis/docx/v1/documents/%s/blocks/%s/children", c.baseURL, docID, docID)
		if err := c.doJSON(ctx, http.MethodPost, api, body, &appended); err != nil {
			return "", fmt.Errorf("写入文档块失败: %w", err)
		}
		if appended.Code != 0 {
			return "", fmt.Errorf("飞书写入文档块失败: code=%d msg=%s", appended.Code, appended.Msg)
		}
	}

	return fmt.Sprintf("https://feishu.cn/docx/%s", docID), nil
}

// --- 鉴权与请求 ---

// accessToken 获取 tenant_access_token,过期前 1 分钟自动续期。
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}

	body, _ := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data string `json:"tenant_access_token"`
		Exp  int    `json:"expire"`
	}
	api := c.baseURL + "/open-apis/auth/v3/tenant_access_token/internal"
	if err := c.doJSON(ctx, http.MethodPost, api, body, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 || resp.Data == "" {
		return "", fmt.Errorf("飞书鉴权失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	c.token = resp.Data
	c.tokenExp = time.Now().Add(time.Duration(resp.Exp)*time.Second - time.Minute)
	return c.token, nil
}

// doJSON 发送 JSON 请求并把响应解析到 out。
func (c *Client) doJSON(ctx context.Context, method, api string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, api, rd)
	if err != nil {
		return fmt.Errorf("构造飞书请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	// 注意:token 请求本身必须先判断后缀再取 token,否则 accessToken 内
	// 调用 doJSON(token API) 会递归调用 accessToken 二次加锁,sync.Mutex
	// 不可重入,形成死锁(表现为 read_feishu_doc 卡住不返回)。
	if !strings.HasSuffix(api, "/tenant_access_token/internal") {
		if token, err := c.accessToken(ctx); err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("飞书请求 %s 失败: %w", api, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("读取飞书响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("飞书请求 %s 返回 HTTP %d: %s", api, resp.StatusCode, truncate(string(data), 300))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("解析飞书响应失败: %w", err)
		}
	}
	return nil
}

// --- 辅助 ---

// blockBatch 每次写入的最大文本块数(飞书限制 children ≤ 50)。
const blockBatch = 30

// maxBlockLen 单个文本块最大字符数(超长切段,留安全余量)。
const maxBlockLen = 7000

// textBlock 构造飞书 text 块(block_type=2)。
func textBlock(content string) map[string]any {
	return map[string]any{
		"block_type": 2,
		"text": map[string]any{
			"elements": []map[string]any{
				{"text_run": map[string]any{"content": content}},
			},
		},
	}
}

// splitContent 把内容拆成不超过 maxBlockLen 的行片段。
func splitContent(content string) []string {
	raw := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var lines []string
	for _, line := range raw {
		runes := []rune(line)
		for len(runes) > maxBlockLen {
			lines = append(lines, string(runes[:maxBlockLen]))
			runes = runes[maxBlockLen:]
		}
		lines = append(lines, string(runes))
	}
	return lines
}

// extractDocToken 从文档 URL 或裸 token 中解析文档 token。
func extractDocToken(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		if u, err := url.Parse(ref); err == nil {
			segs := strings.Split(strings.Trim(u.Path, "/"), "/")
			for i, seg := range segs {
				if (seg == "docx" || seg == "wiki") && i+1 < len(segs) {
					return segs[i+1]
				}
			}
			if len(segs) > 0 && segs[len(segs)-1] != "" {
				return segs[len(segs)-1]
			}
		}
		return ""
	}
	// 裸 token:飞书文档 token 形如 XxxxXxxxXXxx,不含空格与斜杠。
	if strings.ContainsAny(ref, " /\n") {
		return ""
	}
	return ref
}

// truncate 截断字符串(诊断信息用)。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
