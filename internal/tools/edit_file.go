package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var EditFileToolName = "edit_file"

// EditFileTool — 编辑文件（SearchReplace 风格）
// Category: write（串行）
// 备注 internal/tools/edit_file.go
type EditFileTool struct{}

var _ tool.InvokableTool = &EditFileTool{}

func (t *EditFileTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: EditFileToolName,
		Desc: "Edit a file by replacing occurrences of old_string with new_string. " +
			"The old_string must be unique in the file. " +
			"Returns a description of the changes made.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {
				Type:     schema.String,
				Desc:     "The path to the file to edit.",
				Required: true,
			},
			"old_string": {
				Type:     schema.String,
				Desc:     "The text to find and replace in the file.",
				Required: true,
			},
			"new_string": {
				Type:     schema.String,
				Desc:     "The text to replace old_string with.",
				Required: true,
			},
		}),
	}, nil
}

func (t *EditFileTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if args.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	// 读取文件内容
	content, err := os.ReadFile(args.Path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	original := string(content)

	// 检查 old_string 是否存在
	count := strings.Count(original, args.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return "", fmt.Errorf("old_string found %d times, must be unique", count)
	}

	// 执行替换
	newContent := strings.Replace(original, args.OldString, args.NewString, 1)

	// 写回文件
	if err := os.WriteFile(args.Path, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// 返回变更描述 + 具体 diff：模型和前端都需要知道具体改了哪几行
	diff := BuildDiff(original, newContent)
	summary := fmt.Sprintf(
		"Successfully edited %s: %d addition%s and %d removal%s",
		args.Path, diff.Additions, plural(diff.Additions), diff.Removals, plural(diff.Removals),
	)

	return summary + "\n" + diff.Text, nil
}

func (t *EditFileTool) Name() string {
	return EditFileToolName
}

func NewEditFileTool() tool.BaseTool {
	return &EditFileTool{}
}
