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

var WriteFileToolName = "write_file"

// WriteFileTool — 写入/创建文件
// Category: write（串行）
// 备注 internal/tools/write_file.go
type WriteFileTool struct{}

var _ tool.InvokableTool = &WriteFileTool{}

// 示例参数:
//
//	{
//	  "path": "/tmp/hello.go",
//	  "content": "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
//	}
func (t *WriteFileTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: WriteFileToolName,
		Desc: "Write content to a file. Creates the file if it doesn't exist, " +
			"overwrites if it does. Automatically creates parent directories.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": { // 文件路径
				Type:     schema.String,
				Desc:     "The path to the file to write.",
				Required: true,
			},
			"content": { // 写入内容
				Type:     schema.String,
				Desc:     "The content to write to the file.",
				Required: true,
			},
		}),
	}, nil
}

func (t *WriteFileTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	// 自动创建父目录
	dir := filepath.Dir(args.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// 检查文件是否已存在（已存在则读取旧内容，用于后续生成 diff）
	var oldContent string
	fileExists := false
	if data, err := os.ReadFile(args.Path); err == nil {
		fileExists = true
		oldContent = string(data)
	}

	// 写入文件
	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// 新建文件：只报行数；覆盖已有文件：带上具体 diff
	if !fileExists {
		lines := strings.Count(args.Content, "\n")
		return fmt.Sprintf("Successfully created %s (%d line(s))", args.Path, lines), nil
	}
	diff := BuildDiff(oldContent, args.Content)
	if diff.Additions == 0 && diff.Removals == 0 {
		return fmt.Sprintf("Successfully wrote %s (no changes)", args.Path), nil
	}
	summary := fmt.Sprintf(
		"Successfully overwrote %s: %d addition%s and %d removal%s",
		args.Path, diff.Additions, plural(diff.Additions), diff.Removals, plural(diff.Removals),
	)
	return summary + "\n" + diff.Text, nil
}

func (t *WriteFileTool) Name() string {
	return WriteFileToolName
}

func NewWriteFileTool() tool.BaseTool {
	return &WriteFileTool{}
}
