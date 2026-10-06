package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPToolAdapter 将 MCP 工具适配为 Eino InvokableTool 接口
//
// 每个 MCP 工具通过此适配器包装后，可以：
// 1. 传给 ToolSearchTool 作为延迟加载工具
// 2. 直接注册到工具 Registry
type MCPToolAdapter struct {
	serverName string         // MCP Server 名称
	tool       *mcp.Tool      // MCP 工具定义
	manager    *ClientManager // MCP 客户端管理器
}

var _ tool.InvokableTool = &MCPToolAdapter{}

// NewMCPToolAdapter 创建 MCP 工具适配器
func NewMCPToolAdapter(serverName string, mcpTool *mcp.Tool, manager *ClientManager) *MCPToolAdapter {
	return &MCPToolAdapter{
		serverName: serverName,
		tool:       mcpTool,
		manager:    manager,
	}
}

// Info 返回工具信息（给 LLM 看的 schema）
func (a *MCPToolAdapter) Info(ctx context.Context) (*schema.ToolInfo, error) {
	// 将 MCP Tool 的 InputSchema 转换为 Eino 的 ParamsOneOf
	params, err := convertInputSchema(a.tool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to convert schema for tool %s: %w", a.tool.Name, err)
	}

	return &schema.ToolInfo{
		Name:        a.tool.Name,
		Desc:        a.tool.Description,
		ParamsOneOf: params,
	}, nil
}

// InvokableRun 执行工具调用
func (a *MCPToolAdapter) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	// 解析参数
	var args map[string]any
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// 调用 MCP 工具
	result, err := a.manager.CallTool(ctx, a.serverName, a.tool.Name, args)
	if err != nil {
		return "", fmt.Errorf("failed to call MCP tool %s: %w", a.tool.Name, err)
	}

	// 将 CallToolResult 转换为字符串
	return formatCallResult(result)
}

// Name 返回工具名称
func (a *MCPToolAdapter) Name() string {
	return a.tool.Name
}

// convertInputSchema 将 MCP InputSchema 转换为 Eino ParamsOneOf
func convertInputSchema(inputSchema any) (*schema.ParamsOneOf, error) {
	if inputSchema == nil {
		return schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}), nil
	}

	// MCP 的 InputSchema 通常是 map[string]any 格式的 JSON Schema
	schemaJSON, err := json.Marshal(inputSchema)
	if err != nil {
		return nil, err
	}

	// 尝试解析为 properties map
	var schemaMap map[string]any
	if err := json.Unmarshal(schemaJSON, &schemaMap); err != nil {
		return nil, err
	}

	// 提取 properties
	properties, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		// 如果没有 properties，返回空参数
		return schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}), nil
	}

	// 转换为 Eino 的 ParameterInfo
	params := make(map[string]*schema.ParameterInfo)
	requiredMap := buildRequiredMap(schemaMap)

	for name, prop := range properties {
		propMap, ok := prop.(map[string]any)
		if !ok {
			continue
		}
		params[name] = convertProperty(propMap, requiredMap[name])
	}

	return schema.NewParamsOneOfByParams(params), nil
}

// buildRequiredMap 构建 required 字段映射
func buildRequiredMap(schemaMap map[string]any) map[string]bool {
	result := make(map[string]bool)
	required, ok := schemaMap["required"].([]any)
	if !ok {
		return result
	}
	for _, r := range required {
		if name, ok := r.(string); ok {
			result[name] = true
		}
	}
	return result
}

// convertProperty 将单个 property 转换为 Eino ParameterInfo
func convertProperty(propMap map[string]any, required bool) *schema.ParameterInfo {
	info := &schema.ParameterInfo{
		Desc:     getString(propMap, "description"),
		Required: required,
	}

	// 设置类型
	typeStr := getString(propMap, "type")
	switch typeStr {
	case "string":
		info.Type = schema.String
	case "number":
		info.Type = schema.Number
	case "boolean":
		info.Type = schema.Boolean
	case "integer":
		info.Type = schema.Integer
	case "object":
		info.Type = schema.Object
	case "array":
		info.Type = schema.Array
	default:
		info.Type = schema.String
	}

	return info
}

// getString 安全获取 map 中的字符串值
func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// formatCallResult 将 MCP CallToolResult 格式化为字符串
func formatCallResult(result *mcp.CallToolResult) (string, error) {
	if result == nil {
		return "", nil
	}

	// 检查是否有错误
	if result.IsError {
		var errMsg string
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				errMsg += text.Text + "\n"
			}
		}
		if errMsg != "" {
			return "", fmt.Errorf("MCP tool error: %s", errMsg)
		}
		return "", fmt.Errorf("MCP tool returned error")
	}

	// 格式化内容
	var output string
	for _, content := range result.Content {
		switch c := content.(type) {
		case *mcp.TextContent:
			output += c.Text
		case *mcp.ImageContent:
			output += fmt.Sprintf("[Image: %s]", c.MIMEType)
		case *mcp.ResourceLink:
			output += fmt.Sprintf("[Resource: %s]", c.URI)
		}
	}

	return output, nil
}

// ConvertMCPTools 批量转换 MCP 工具为 Eino 工具
func ConvertMCPTools(manager *ClientManager) []tool.BaseTool {
	var tools []tool.BaseTool

	for _, server := range manager.GetServers() {
		// 获取该 Server 的所有工具
		ctx := context.Background()
		result, err := server.Session.ListTools(ctx, nil)
		if err != nil {
			continue
		}

		for _, mcpTool := range result.Tools {
			adapter := NewMCPToolAdapter(server.Name, mcpTool, manager)
			tools = append(tools, adapter)
		}
	}

	return tools
}
