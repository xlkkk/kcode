package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var BashToolName = "bash"

// BashTool — 执行 Shell 命令
// Category: command（串行）
// 备注 internal/tools/bash.go
type BashTool struct {
	Timeout time.Duration // 命令超时时间，默认 120s
}

var _ tool.InvokableTool = &BashTool{}

func (t *BashTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: BashToolName,
		Desc: "Execute a shell command and return its output. The command runs in the current working directory.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {
				Type:     schema.String,
				Desc:     "The shell command to execute.",
				Required: true,
			},
		}),
	}, nil
}

func (t *BashTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second // 兜底
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", args.Command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// 组合输出结果
	result := stdout.String()
	if stderr.Len() > 0 {
		if result != "" {
			result += "\n"
		}
		result += "STDERR:\n" + stderr.String()
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return result + "\n[timeout: command exceeded " + timeout.String() + "]", nil
		}
		return result + fmt.Sprintf("\n[exit code: %d]", cmd.ProcessState.ExitCode()), nil
	}

	if result == "" {
		result = "[command executed successfully with no output]"
	}

	return result, nil
}

func (t *BashTool) Name() string {
	return BashToolName
}

func NewBashTool() tool.BaseTool {
	return &BashTool{}
}
