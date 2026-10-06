// workflow_tools.go — 工作流控制工具。
//
// 三个工具共同实现「强制流程」的推进与人工审核闭环:
//   - request_review:  模块划分文档的人工审核门(专用 ReviewCh 链路,携带
//     文档内容与流程节点,前端可渲染加粗当前阶段的升级版审核面板)
//   - advance_stage:   阶段推进唯一入口,内部校验退出条件(产出物/审核/编译/测试)
//   - workflow_status: 查看当前阶段、产出物与审核状态
//
// 工具名与 workflow 包的 ToolRequestReview/ToolAdvanceStage/ToolWorkflowStatus
// 保持一致;注册发生在 Agent 工具定格之后(见 internal/agent/agent.go),
// 因此这些工具只对 Lead 可见,子 Agent 无法跳阶段。

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"kcode/internal/workflow"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	RequestReviewToolName  = "request_review"
	AdvanceStageToolName   = "advance_stage"
	WorkflowStatusToolName = "workflow_status"
)

// ReviewRequest 发射给交互层的审核请求:携带审核对象文档内容与流程节点
// (StageNodes 标记 Current/Done,前端据 Current 加粗当前流程节点)。
type ReviewRequest struct {
	ArtifactPath    string               // 审核对象路径(如 docs/modules.md)
	ArtifactContent string               // 文档内容(前端直接渲染预览)
	Stages          []workflow.StageNode // 全部流程节点及当前/已完成标记
	ResponseCh      chan ReviewResponse
}

// ReviewResponse 用户审核决策。
type ReviewResponse struct {
	Approved bool
	Feedback string // 驳回意见
}

// --- RequestReviewTool ---

// RequestReviewTool 人工审核门:把模块文档(含内容)发射到交互层(ReviewCh),
// 阻塞等待用户「通过/驳回」决策后写回状态机。
type RequestReviewTool struct {
	ch    chan<- ReviewRequest
	state *workflow.WorkflowState
	store *workflow.Store
	dir   string
}

var _ tool.InvokableTool = &RequestReviewTool{}

// NewRequestReviewTool 创建审核工具。ch 是专用审核发射通道(Web 层监听,
// 转成 review_request 事件);dir 用于读取审核对象文档内容。
func NewRequestReviewTool(ch chan<- ReviewRequest, state *workflow.WorkflowState, store *workflow.Store, dir string) tool.BaseTool {
	return &RequestReviewTool{ch: ch, state: state, store: store, dir: dir}
}

func (t *RequestReviewTool) Name() string { return RequestReviewToolName }

func (t *RequestReviewTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        RequestReviewToolName,
		Desc:        `Submit docs/modules.md for mandatory human review. The user approves or rejects the module breakdown; on rejection, revise the document according to the feedback and submit again. You cannot advance to code_gen without approval.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *RequestReviewTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if t.state.Stage != workflow.StageModuleDesign {
		return "", fmt.Errorf("request_review 只能在 module_design 阶段调用(当前 %s)", t.state.Stage)
	}
	if t.ch == nil {
		return "", fmt.Errorf("request_review 不可用:当前环境没有审核交互通道")
	}

	content, err := os.ReadFile(filepath.Join(t.dir, filepath.FromSlash(workflow.ArtifactModules)))
	if err != nil {
		return "", fmt.Errorf("读取审核对象 %s 失败: %w", workflow.ArtifactModules, err)
	}

	respCh := make(chan ReviewResponse, 1)
	select {
	case t.ch <- ReviewRequest{
		ArtifactPath:    workflow.ArtifactModules,
		ArtifactContent: string(content),
		Stages:          workflow.StageNodes(t.state),
		ResponseCh:      respCh,
	}:
	case <-ctx.Done():
		return "", fmt.Errorf("review cancelled: %w", ctx.Err())
	}
	workflow.RecordTimeline(t.state, t.state.Stage, workflow.EventReviewSubmitted, workflow.ArtifactModules)
	if t.store != nil {
		_ = t.store.Save(t.state)
	}

	res := ""
	select {
	case resp := <-respCh:
		if resp.Approved {
			t.state.Review = workflow.ReviewApproved
			t.state.ReviewFeedback = ""
			workflow.RecordTimeline(t.state, t.state.Stage, workflow.EventReviewApproved, workflow.ArtifactModules)
		} else {
			t.state.Review = workflow.ReviewRejected
			t.state.ReviewFeedback = resp.Feedback
			workflow.RecordTimeline(t.state, t.state.Stage, workflow.EventReviewRejected, resp.Feedback)
		}
		if t.store != nil {
			_ = t.store.Save(t.state)
		}
		if t.state.Review == workflow.ReviewApproved {
			res = "审核已通过:可以调用 advance_stage 进入 code_gen 阶段。"
		} else {
			feedback := t.state.ReviewFeedback
			if feedback == "" {
				feedback = "(未提供意见)"
			}
			res = "审核被驳回,修改意见: " + feedback + "。请修改 docs/modules.md 后再次调用 request_review。"
		}
	case <-ctx.Done():
		return "", fmt.Errorf("review cancelled: %w", ctx.Err())
	}
	return res, nil
}

// --- AdvanceStageTool ---

// AdvanceStageTool 阶段推进唯一入口:校验退出条件(含 go build / go test)
// 通过后才更新状态并落盘,LLM 无法自行跳阶段。
type AdvanceStageTool struct {
	def   workflow.WorkflowDef
	state *workflow.WorkflowState
	store *workflow.Store
	dir   string
}

var _ tool.InvokableTool = &AdvanceStageTool{}

// NewAdvanceStageTool 创建推进工具。def 是流程定义(config 下沉);
// dir 是项目 workDir(产物检测与编译测试用)。
func NewAdvanceStageTool(def workflow.WorkflowDef, state *workflow.WorkflowState, store *workflow.Store, dir string) tool.BaseTool {
	return &AdvanceStageTool{def: def, state: state, store: store, dir: dir}
}

func (t *AdvanceStageTool) Name() string { return AdvanceStageToolName }

func (t *AdvanceStageTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        AdvanceStageToolName,
		Desc:        `Advance the workflow to the next stage. This is the ONLY way to move stages; it validates exit criteria (artifacts, human review approval, go build / go test) and refuses to advance otherwise.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *AdvanceStageTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	next, err := workflow.Advance(t.def, t.state, t.dir)
	if err != nil {
		return "", err
	}
	if t.store != nil {
		_ = t.store.Save(t.state)
	}
	return fmt.Sprintf("已推进到 %s 阶段:%s\n\n%s", next, workflow.StageDescriptions[next], workflow.StageConstraint(t.state)), nil
}

// --- WorkflowStatusTool ---

// WorkflowStatusTool 查看当前阶段、产出物与审核状态。
type WorkflowStatusTool struct {
	state *workflow.WorkflowState
}

var _ tool.InvokableTool = &WorkflowStatusTool{}

// NewWorkflowStatusTool 创建状态查询工具。
func NewWorkflowStatusTool(state *workflow.WorkflowState) tool.BaseTool {
	return &WorkflowStatusTool{state: state}
}

func (t *WorkflowStatusTool) Name() string { return WorkflowStatusToolName }

func (t *WorkflowStatusTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        WorkflowStatusToolName,
		Desc:        `Show the current workflow stage, produced artifacts, review status, and what must happen next.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *WorkflowStatusTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	artifacts := map[string]string{}
	for st, p := range t.state.Artifacts {
		artifacts[string(st)] = p
	}
	status := map[string]any{
		"stage":             t.state.Stage,
		"stage_desc":        workflow.StageDescriptions[t.state.Stage],
		"next_stage":        workflow.NextStage(t.state.Stage),
		"plan_loaded":       t.state.PlanLoaded,
		"review":            t.state.Review,
		"review_feedback":   t.state.ReviewFeedback,
		"simulation_passed": t.state.SimulationPassed,
		"artifacts":         artifacts,
		"timeline":          t.state.Timeline,
		"constraint":        workflow.StageConstraint(t.state),
	}
	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal workflow status: %w", err)
	}
	return string(b), nil
}
