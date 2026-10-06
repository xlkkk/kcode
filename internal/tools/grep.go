package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var GrepToolName = "grep"

// GrepTool — 正则表达式内容检索
// Category: read（可并发）
// 备注 internal/tools/grep.go
type GrepTool struct{}

var _ tool.InvokableTool = &GrepTool{}

/*
	{
	  "pattern": "func.*Agent",
	  "path": "./internal",
	  "include": "*.go"
	}
*/
func (t *GrepTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: GrepToolName,
		Desc: "Search for a pattern in files using regular expressions. " +
			"Returns matching lines with file paths and line numbers.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": { // 正则表达式（file content匹配）
				Type:     schema.String,
				Desc:     "The regex pattern to search for.",
				Required: true,
			},
			"path": { // 搜索路径
				Type:     schema.String,
				Desc:     "The file or directory to search in. Defaults to current directory.",
				Required: false,
			},
			"include": { // 在哪些文件中搜索
				Type:     schema.String,
				Desc:     "File pattern to include (e.g., '*.go'). Only matches files with this pattern.",
				Required: false,
			},
		}),
	}, nil
}

func (t *GrepTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// 编译正则
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	// 确定搜索路径
	searchPath := args.Path
	if searchPath == "" {
		searchPath = "."
	}

	// 获取文件列表
	var files []string
	info, err := os.Stat(searchPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat path: %w", err)
	}

	// 1. 遍历目录
	if info.IsDir() {
		filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				// 跳过隐藏目录和常见非代码目录
				name := info.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			// 检查 include 模式
			// 第 1 层: include 过滤文件名
			if args.Include != "" {
				matched, _ := filepath.Match(args.Include, info.Name())
				if !matched {
					return nil
				}
			}
			files = append(files, path)
			return nil
		})
	} else {
		files = []string{searchPath}
	}

	// 搜索文件
	var results []string
	maxResults := 100 // 限制结果数量
	count := 0

	for _, file := range files {
		if count >= maxResults {
			results = append(results, fmt.Sprintf("... (truncated, showing first %d matches)", maxResults))
			break
		}

		matches, err := searchInFile(file, re)
		if err != nil {
			continue // 跳过无法读取的文件
		}

		for _, match := range matches {
			if count >= maxResults {
				break
			}
			results = append(results, match)
			count++
		}
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	return strings.Join(results, "\n"), nil
}

// searchInFile 在文件中搜索匹配行
func searchInFile(path string, re *regexp.Regexp) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var results []string
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) { // 第 2 层: pattern 过滤文件内容
			results = append(results, fmt.Sprintf("%s:%d: %s", path, lineNum, line))
		}
	}

	return results, scanner.Err()
}

func (t *GrepTool) Name() string {
	return GrepToolName
}

func NewGrepTool() tool.BaseTool {
	return &GrepTool{}
}
