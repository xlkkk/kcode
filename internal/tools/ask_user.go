package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var AskUserToolName = "AskUserQuestion"

// AskUserToolName — 用户交互工具（HITL）
//
// LLM 调用本工具向用户提出结构化选择题。工具本身不直接与人交互，
// 而是通过 RequestCh 向外发射 AskUserRequest，由交互层（Web Server）
// 推送到前端、收集回答后写入 ResponseCh，工具阻塞等待结果。
//
// 备注 internal/tools/ask_user.go

// QuestionOption 单个选项
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Question 一个问题（1-4 个选项）
type Question struct {
	Text        string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

// QuestionResponse 用户回答：question text → answer
type QuestionResponse struct {
	Answers map[string]string
}

// AskUserRequest 发射给交互层的请求
type AskUserRequest struct {
	Questions  []Question
	ResponseCh chan QuestionResponse
}

// AskUserTool 实现 Eino tool.InvokableTool
type AskUserTool struct {
	RequestCh chan<- AskUserRequest
}

var _ tool.InvokableTool = &AskUserTool{}

func NewAskUserTool(requestCh chan<- AskUserRequest) tool.BaseTool {
	return &AskUserTool{RequestCh: requestCh}
}

func (t *AskUserTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: AskUserToolName,
		Desc: `Ask the user a question with structured multiple-choice options. Use this to:
- Gather user preferences or requirements
- Clarify ambiguous instructions
- Get decisions on implementation choices
- Offer choices about direction to take

Each question has 2-4 options. An "Other" option for custom input is automatically provided.
Use multiSelect: true when choices are not mutually exclusive.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"questions": {
				Type:     schema.Array,
				Desc:     "The questions to ask (1-4 questions).",
				Required: true,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.Object,
					SubParams: map[string]*schema.ParameterInfo{
						"question": {
							Type:     schema.String,
							Desc:     "The question to ask the user",
							Required: true,
						},
						"header": {
							Type:     schema.String,
							Desc:     "Very short label displayed as a chip (max 12 chars)",
							Required: true,
						},
						"options": {
							Type: schema.Array,
							Desc: "2-4 available choices, each with label and description.",
							ElemInfo: &schema.ParameterInfo{
								Type: schema.Object,
								SubParams: map[string]*schema.ParameterInfo{
									"label": {
										Type:     schema.String,
										Desc:     "Option display text (1-5 words)",
										Required: true,
									},
									"description": {
										Type:     schema.String,
										Desc:     "Explanation of what this option means",
										Required: true,
									},
								},
							},
						},
						"multiSelect": {
							Type:     schema.Boolean,
							Desc:     "Set to true to allow multiple selection",
							Required: true,
						},
					},
				},
			},
		}),
	}, nil
}

func (t *AskUserTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Questions []Question `json:"questions"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	questions := args.Questions
	if len(questions) == 0 || len(questions) > 4 {
		return "", fmt.Errorf("must have 1-4 questions")
	}
	for _, q := range questions {
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return "", fmt.Errorf("question %q must have 2-4 options", q.Text)
		}
	}

	if t.RequestCh == nil {
		return "", fmt.Errorf("AskUserQuestion not available in this context")
	}

	respCh := make(chan QuestionResponse, 1)
	select {
	case t.RequestCh <- AskUserRequest{
		Questions:  questions,
		ResponseCh: respCh,
	}:
	case <-ctx.Done():
		return "", fmt.Errorf("question cancelled: %w", ctx.Err())
	}

	select {
	case resp := <-respCh:
		var parts []string
		for q, a := range resp.Answers {
			parts = append(parts, fmt.Sprintf("%q = %q", q, a))
		}
		return fmt.Sprintf("User has answered your questions: %s. You can now continue with the user's answers in mind.", strings.Join(parts, ", ")), nil
	case <-ctx.Done():
		return "", fmt.Errorf("question cancelled: %w", ctx.Err())
	}
}

func (t *AskUserTool) Name() string {
	return AskUserToolName
}
