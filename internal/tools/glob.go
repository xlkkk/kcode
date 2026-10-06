package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var GlobToolName = "glob"

// GlobTool — 按 glob 模式搜索文件
// Category: read（可并发）
// 备注 internal/tools/glob.go
type GlobTool struct{}

var _ tool.InvokableTool = &GlobTool{}

// 示例参数:
//
//	{
//	  "pattern": "**/*.go",
//	  "path": "./internal"
//	}
func (t *GlobTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: GlobToolName,
		Desc: "Find files matching a glob pattern. Returns a list of matching file paths.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {
				Type:     schema.String,
				Desc:     "The glob pattern to match files (e.g., '*.go', '**/*.go', 'internal/*.go').",
				Required: true,
			},
			"path": {
				Type:     schema.String,
				Desc:     "The directory to search in. Defaults to current directory.",
				Required: false,
			},
		}),
	}, nil
}

func (t *GlobTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}

	// 检查路径是否存在
	info, err := os.Stat(searchPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", searchPath)
	}

	// 处理 ** 递归匹配
	var results []string
	maxResults := 200

	if strings.Contains(args.Pattern, "**") {
		// ** 模式：递归搜索
		results = globRecursive(searchPath, args.Pattern, maxResults)
	} else {
		// 普通 glob：只在当前目录匹配
		fullPattern := filepath.Join(searchPath, args.Pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("invalid glob pattern: %w", err)
		}
		results = matches
	}

	if len(results) == 0 {
		return "No files found.", nil
	}

	if len(results) >= maxResults {
		results = results[:maxResults]
		results = append(results, fmt.Sprintf("... (truncated, showing first %d matches)", maxResults))
	}

	return strings.Join(results, "\n"), nil
}

// globRecursive 递归搜索匹配 ** 模式的文件
func globRecursive(root, pattern string, maxResults int) []string {
	// 将 ** 替换为递归匹配逻辑
	// 例如 "**/*.go" → 匹配任意深度的 .go 文件
	parts := strings.Split(pattern, "**")
	prefix := parts[0] // ** 之前的部分
	suffix := ""       // ** 之后的部分
	if len(parts) > 1 {
		suffix = strings.TrimPrefix(parts[1], string(filepath.Separator))
	}

	// 去掉 prefix 开头的分隔符
	prefix = strings.TrimPrefix(prefix, string(filepath.Separator))
	prefix = strings.TrimSuffix(prefix, string(filepath.Separator))

	startDir := root
	if prefix != "" {
		startDir = filepath.Join(root, prefix)
	}

	var results []string
	filepath.Walk(startDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if len(results) >= maxResults {
			return filepath.SkipAll
		}

		// 跳过隐藏目录
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && path != startDir {
			return filepath.SkipDir
		}

		if info.IsDir() {
			return nil
		}

		// 检查文件是否匹配 suffix 模式
		if suffix != "" {
			matched, _ := filepath.Match(suffix, info.Name())
			if !matched {
				return nil
			}
		}

		results = append(results, path)
		return nil
	})

	return results
}

func (t *GlobTool) Name() string {
	return GlobToolName
}

func NewGlobTool() tool.BaseTool {
	return &GlobTool{}
}
