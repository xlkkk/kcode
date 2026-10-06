package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var ReadFileToolName = "read_file"

// ReadFileTool — 读取文件内容
// Category: read（可并发）
// 备注 internal/tools/read_file.go
type ReadFileTool struct{}

var _ tool.InvokableTool = &ReadFileTool{}

func (t *ReadFileTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ReadFileToolName,
		Desc: "Read the contents of a file at the specified path. Returns the file content as a string.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {
				Type:     schema.String,
				Desc:     "The absolute or relative path to the file to read.",
				Required: true,
			},
		}),
	}, nil
}

// 这里直接返回非json的string结构，方便查看文件内容，其他场景下返回结构化数据更好
func (t *ReadFileTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	content, err := os.ReadFile(args.Path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(content), nil
}

func (t *ReadFileTool) Name() string {
	return ReadFileToolName
}

func NewReadFileTool() tool.BaseTool {
	return &ReadFileTool{}
}
