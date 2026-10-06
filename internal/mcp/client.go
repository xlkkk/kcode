package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientManager MCP 客户端管理器
//
// 管理多个 MCP Server 的连接和工具发现。
// 支持 stdio（子进程）和 URL（HTTP）两种传输方式。
type ClientManager struct {
	configs []MCPServerConfig
	servers []*ServerConnection
	mu      sync.RWMutex
}

// ServerConnection 单个 MCP Server 的连接
type ServerConnection struct {
	Name    string
	Client  *mcp.Client
	Session *mcp.ClientSession
	Config  MCPServerConfig
}

// NewClientManager 创建 MCP 客户端管理器
func NewClientManager(configs []MCPServerConfig) *ClientManager {
	return &ClientManager{
		configs: configs,
		servers: make([]*ServerConnection, 0),
	}
}

// ConnectAll 连接所有配置的 MCP Server
func (m *ClientManager) ConnectAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, cfg := range m.configs {
		conn, err := connectServer(ctx, cfg)
		if err != nil {
			// 连接失败不阻断启动，只记录错误
			continue
		}
		m.servers = append(m.servers, conn)
	}
	return nil
}

// connectServer 连接单个 MCP Server
func connectServer(ctx context.Context, cfg MCPServerConfig) (*ServerConnection, error) {
	// 创建 MCP 客户端
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "kcode",
		Version: "0.1.0",
	}, nil)

	// 根据配置选择传输方式：Command 优先，其次 URL
	var transport mcp.Transport
	if cfg.Command != "" {
		// stdio 方式：启动子进程
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if cfg.WorkingDir != "" {
			cmd.Dir = cfg.WorkingDir
		}
		if len(cfg.Env) > 0 {
			env := cmd.Environ()
			for k, v := range cfg.Env {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
			cmd.Env = env
		}
		transport = &mcp.CommandTransport{Command: cmd}
	} else if cfg.URL != "" {
		// URL 方式：Streamable HTTP
		transport = &mcp.StreamableClientTransport{
			Endpoint: cfg.URL,
		}
	} else {
		return nil, fmt.Errorf("MCP server %s: must specify either command or url", cfg.Name)
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MCP server %s: %w", cfg.Name, err)
	}

	return &ServerConnection{
		Name:    cfg.Name,
		Client:  client,
		Session: session,
		Config:  cfg,
	}, nil
}

// ListAllTools 获取所有已连接 Server 的工具
func (m *ClientManager) ListAllTools(ctx context.Context) ([]*mcp.Tool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var allTools []*mcp.Tool
	for _, conn := range m.servers {
		result, err := conn.Session.ListTools(ctx, nil)
		if err != nil {
			continue
		}
		allTools = append(allTools, result.Tools...)
	}
	return allTools, nil
}

// CallTool 调用指定 Server 上的工具
func (m *ClientManager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 查找对应的 Server
	for _, conn := range m.servers {
		if conn.Name == serverName {
			return conn.Session.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: args,
			})
		}
	}
	return nil, fmt.Errorf("MCP server %s not found", serverName)
}

// Close 关闭所有连接
func (m *ClientManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.servers {
		// Client 没有直接的 Close 方法，通过 Session 关闭
		if conn.Session != nil {
			_ = conn.Session.Close()
		}
	}
	m.servers = nil
	return nil
}

// GetServers 获取所有已连接的 Server
func (m *ClientManager) GetServers() []*ServerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.servers
}
