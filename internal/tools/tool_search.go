package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var ToolSearchToolName = "tool_search"

// ToolSearchTool — 延迟工具检索
// Category: read（可并发）
// 模型通过此工具搜索并加载延迟加载的 MCP 工具 schema
// 备注 internal/tools/tool_search.go
type ToolSearchTool struct {
	deferredTools []tool.BaseTool // 延迟加载的工具列表
}

var _ tool.InvokableTool = &ToolSearchTool{}

// NewToolSearchTool 创建延迟工具检索工具
func NewToolSearchTool(deferredTools []tool.BaseTool) *ToolSearchTool {
	return &ToolSearchTool{
		deferredTools: deferredTools,
	}
}

// SetDeferredTools 设置延迟工具列表（支持延迟绑定）
func (t *ToolSearchTool) SetDeferredTools(tools []tool.BaseTool) {
	t.deferredTools = tools
}

// 示例参数:
//
//	{
//	  "query": "database"
//	}
func (t *ToolSearchTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ToolSearchToolName,
		Desc: "Search for available tools by name or description keyword. " +
			"Returns matching tool schemas that can then be used. " +
			"Use 'select:ToolName' for exact name matching.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "Search keyword to find relevant tools (matches tool name or description).",
				Required: true,
			},
		}),
	}, nil
}

func (t *ToolSearchTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	// select: 模式：精确匹配工具名
	if strings.HasPrefix(args.Query, "select:") {
		names := strings.Split(strings.TrimPrefix(args.Query, "select:"), ",")
		return t.selectByNames(ctx, names)
	}

	// 关键词搜索模式
	query := strings.ToLower(args.Query)
	var results []string

	for _, t := range t.deferredTools {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}

		// 搜索工具名和描述
		nameMatch := strings.Contains(strings.ToLower(info.Name), query)
		descMatch := strings.Contains(strings.ToLower(info.Desc), query)

		if nameMatch || descMatch {
			// 序列化工具 schema
			infoJSON, err := json.MarshalIndent(info, "", "  ")
			if err != nil {
				continue
			}
			results = append(results, string(infoJSON))
		}
	}

	if len(results) == 0 {
		// 搜索无结果时，列出所有可用延迟工具名字
		available := t.getDeferredNames()
		if len(available) == 0 {
			return fmt.Sprintf("No tools found matching '%s'. No deferred tools available.", args.Query), nil
		}
		return fmt.Sprintf("No tools found matching '%s'. Available deferred tools: %s",
			args.Query, available), nil
	}

	return strings.Join(results, "\n\n"), nil
}

// selectByNames 精确匹配工具名
func (t *ToolSearchTool) selectByNames(ctx context.Context, names []string) (string, error) {
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[strings.TrimSpace(strings.ToLower(n))] = true
	}

	var results []string
	for _, tool := range t.deferredTools {
		info, err := tool.Info(ctx)
		if err != nil {
			continue
		}
		if nameSet[strings.ToLower(info.Name)] {
			infoJSON, err := json.MarshalIndent(info, "", "  ")
			if err != nil {
				continue
			}
			results = append(results, string(infoJSON))
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("Tools not found: %v", names), nil
	}
	return strings.Join(results, "\n\n"), nil
}

// getDeferredNames 获取所有延迟工具的名字列表
func (t *ToolSearchTool) getDeferredNames() string {
	var names []string
	for _, tool := range t.deferredTools {
		info, err := tool.Info(context.Background())
		if err != nil {
			continue
		}
		names = append(names, info.Name)
	}
	return strings.Join(names, ", ")
}

func (t *ToolSearchTool) Name() string {
	return ToolSearchToolName
}
