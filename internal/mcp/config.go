package mcp

// MCPServerConfig MCP 服务器配置
//
// 对应 .kcode/config.yaml 中的 mcp_servers 配置项。
// 支持两种传输方式：
//   - stdio: 通过子进程通信（command + args）
//   - url:   通过 HTTP 连接远程服务（Streamable HTTP / SSE）
//
// 示例配置:
//
//	mcp_servers:
//	  # stdio 方式（本地子进程）
//	  - name: filesystem
//	    command: npx
//	    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
//
//	  # url 方式（远程服务）
//	  - name: remote-tools
//	    url: https://mcp.example.com/mcp
type MCPServerConfig struct {
	// 服务器名称（唯一标识）
	Name string `yaml:"name"`

	// stdio 方式：启动命令（如 npx, node, python）
	// 与 URL 二选一，Command 优先
	Command string `yaml:"command,omitempty"`
	// stdio 方式：命令参数
	Args []string `yaml:"args,omitempty"`
	// stdio 方式：环境变量
	Env map[string]string `yaml:"env,omitempty"`
	// stdio 方式：工作目录（可选，默认使用当前目录）
	WorkingDir string `yaml:"working_dir,omitempty"`

	// url 方式：远程 MCP Server 地址（Streamable HTTP / SSE）
	// 与 Command 二选一，Command 优先
	URL string `yaml:"url,omitempty"`
}
