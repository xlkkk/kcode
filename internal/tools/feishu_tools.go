// feishu_tools.go — 飞书读写工具(client 模式)。
//
// 对应 game.md 的输入输出链路:
//   - read_feishu_doc:  init 阶段读取策划案,成功读取置位 state.PlanLoaded
//     (workflow.require_plan 为 true 时,advance_stage 校验该标记)
//   - write_feishu_doc: qa_cases 阶段把 docs/qa_cases.md 写回飞书
//     (WorkflowGateMW 只在 qa_cases/done 放行该工具)
//
// 注册发生在 Agent 工具定格之后(见 internal/agent/agent.go),
// 与工作流工具一致:Lead 专属,子 Agent 不可见。

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"kcode/internal/feishu"
	"kcode/internal/workflow"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	ReadFeishuDocToolName  = "read_feishu_doc"
	WriteFeishuDocToolName = "write_feishu_doc"
)

// --- ReadFeishuDocTool ---

// ReadFeishuDocTool 读取飞书策划案文档(URL 或 token),成功读取后
// 置位 state.PlanLoaded(state 为 nil 时仅返回内容,不强制流程)。
type ReadFeishuDocTool struct {
	client *feishu.Client
	state  *workflow.WorkflowState
	store  *workflow.Store
}

var _ tool.InvokableTool = &ReadFeishuDocTool{}

// NewReadFeishuDocTool 创建读取工具。state/store 仅在 workflow.enabled 时非 nil。
func NewReadFeishuDocTool(client *feishu.Client, state *workflow.WorkflowState, store *workflow.Store) tool.BaseTool {
	return &ReadFeishuDocTool{client: client, state: state, store: store}
}

func (t *ReadFeishuDocTool) Name() string { return ReadFeishuDocToolName }

func (t *ReadFeishuDocTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ReadFeishuDocToolName,
		Desc: `Read a Feishu docx document (game design doc / 策划案) by URL or document token, returning its plain-text content. Call this in the init stage: with require_plan enabled the workflow refuses to advance until the plan has been read successfully.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"doc": {
				Type:     schema.String,
				Desc:     "Feishu docx URL (e.g. https://xxx.feishu.cn/docx/TOKEN) or the bare document token",
				Required: true,
			},
		}),
	}, nil
}

func (t *ReadFeishuDocTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Doc string `json:"doc"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Doc == "" {
		return "", fmt.Errorf("缺少 doc 参数:飞书文档 URL 或 token")
	}

	content, err := t.client.ReadDocRaw(ctx, args.Doc)
	if err != nil {
		return "", err
	}
	if t.state != nil {
		t.state.PlanLoaded = true
		if t.store != nil {
			_ = t.store.Save(t.state)
		}
	}
	return content, nil
}

// --- WriteFeishuDocTool ---

// WriteFeishuDocTool 把本地文档写回飞书:新建云文档并写入内容,返回文档 URL。
// 默认目标为 docs/qa_cases.md(QA 案例写回),也可显式指定其他本地文件。
type WriteFeishuDocTool struct {
	client *feishu.Client
	dir    string
}

var _ tool.InvokableTool = &WriteFeishuDocTool{}

// NewWriteFeishuDocTool 创建写回工具。dir 是项目 workDir(解析相对路径用)。
func NewWriteFeishuDocTool(client *feishu.Client, dir string) tool.BaseTool {
	return &WriteFeishuDocTool{client: client, dir: dir}
}

func (t *WriteFeishuDocTool) Name() string { return WriteFeishuDocToolName }

func (t *WriteFeishuDocTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: WriteFeishuDocToolName,
		Desc: `Write a local document back to Feishu: creates a new Feishu docx containing the file's content and returns its URL. Default target is docs/qa_cases.md (QA test cases). Only available in the qa_cases stage.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {
				Type:     schema.String,
				Desc:     "Local file path to write back, relative to the project root (default: docs/qa_cases.md)",
				Required: false,
			},
			"title": {
				Type:     schema.String,
				Desc:     "Title of the created Feishu document (default: 'QA 测试案例')",
				Required: false,
			},
			"folder_token": {
				Type:     schema.String,
				Desc:     "Optional target Feishu folder token",
				Required: false,
			},
		}),
	}, nil
}

func (t *WriteFeishuDocTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Path        string `json:"path"`
		Title       string `json:"title"`
		FolderToken string `json:"folder_token"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	path := args.Path
	if path == "" {
		path = workflow.ArtifactQACases
	}
	data, err := os.ReadFile(filepath.Join(t.dir, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	title := args.Title
	if title == "" {
		title = "QA 测试案例"
	}

	url, err := t.client.CreateDoc(ctx, title, string(data), args.FolderToken)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已将 %s 写回飞书,文档链接: %s", path, url), nil
}
