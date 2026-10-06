// workflow.go — 游戏开发工作流状态机类型定义。
//
// 五阶段强制流水线:init(读取策划案)→ flow_analysis(流程梳理)
// → module_design(模块划分,需人工审核)→ code_gen(代码生成)
// → self_test(自测)→ qa_cases(QA 测试案例)→ done。
//
// 强制机制由三层配合实现:
//   - WorkflowGateMW 按当前阶段拦截写类工具(工具矩阵门禁)
//   - advance_stage 工具调用 CanAdvance/VerifyTransition 校验退出条件
//   - 状态持久化到 .kcode/workflow.json,支持断点续跑
//
// 设计文档见项目根目录 game.md。

package workflow

import (
	"fmt"
	"time"
)

// Stage 工作流阶段
type Stage string

const (
	StageInit         Stage = "init"          // 初始:策划案未读取
	StageFlowAnalysis Stage = "flow_analysis" // 流程梳理
	StageModuleDesign Stage = "module_design" // 模块划分(需人工审核)
	StageCodeGen      Stage = "code_gen"      // 代码生成(游戏代码=最终产物)
	StageSelfTest     Stage = "self_test"     // 自测
	StageQACases      Stage = "qa_cases"      // QA 测试案例生成
	StageDone         Stage = "done"          // 流程终态
)

// StageDescriptions 阶段的人类可读描述(注入给 LLM 与用户)
var StageDescriptions = map[Stage]string{
	StageInit:         "读取策划案:从用户消息(后续接入飞书)获取策划案内容,未读取禁止推进",
	StageFlowAnalysis: "流程梳理:产出 docs/flow.md(玩法规则、房间状态机、消息流、结算逻辑、边界情况)",
	StageModuleDesign: "模块划分:产出 docs/modules.md(模块职责/接口/依赖、数据模型、协议设计),必须提交人工审核",
	StageCodeGen:      "代码生成:按审核通过的模块文档实现游戏服务端代码(最终产物),go build 通过",
	StageSelfTest:     "自测:编写测试并跑 go test,全绿才可推进",
	StageQACases:      "QA 测试案例:产出 docs/qa_cases.md(用例表:前置条件/步骤/预期),后续写回飞书",
	StageDone:         "流程终态:全部产出物已就绪",
}

// ReviewStatus 人工审核状态
type ReviewStatus string

const (
	ReviewNone     ReviewStatus = "none"     // 未提交审核
	ReviewPending  ReviewStatus = "pending"  // 已提交,等待用户决策
	ReviewApproved ReviewStatus = "approved" // 审核通过
	ReviewRejected ReviewStatus = "rejected" // 审核驳回(意见见 ReviewFeedback)
)

// Artifact 产出物相对路径常量(相对项目 workDir)
const (
	ArtifactFlow    = "docs/flow.md"     // 流程梳理产出
	ArtifactModules = "docs/modules.md"  // 模块划分产出(审核对象)
	ArtifactQACases = "docs/qa_cases.md" // QA 案例产出
)

// WorkflowState 工作流运行态(项目级共享,跨会话断点续跑)
type WorkflowState struct {
	Stage Stage `json:"stage"`

	// PlanLoaded 表示策划案已读取(由 read_feishu_doc 置位;RequirePlan
	// 为 true 时 init → flow_analysis 必须校验该标记)。
	PlanLoaded bool `json:"plan_loaded,omitempty"`

	// Review 是模块划分的人工审核状态;ReviewFeedback 存驳回意见。
	Review         ReviewStatus `json:"review"`
	ReviewFeedback string       `json:"review_feedback,omitempty"`

	// Artifacts 记录各阶段产出物路径(存在即已产出)。
	Artifacts map[Stage]string `json:"artifacts,omitempty"`

	// SimulationPassed 表示对局模拟达标(由 simulate_match 置位;RequireSimulation
	// 为 true 时 self_test → qa_cases 必须校验该标记)。SimulationReport
	// 存最近一次模拟的摘要结论(供 workflow_status / 前端时间线展示)。
	SimulationPassed bool   `json:"simulation_passed,omitempty"`
	SimulationReport string `json:"simulation_report,omitempty"`

	// Timeline 是流程事件时间线(阶段进入/产出物/审核/模拟),供前端渲染
	// 流程时间线可视化;由 RecordTimeline 追加,上限 maxTimelineEvents。
	Timeline []TimelineEvent `json:"timeline,omitempty"`
}

// NewState 创建初始状态
func NewState() *WorkflowState {
	return &WorkflowState{
		Stage:     StageInit,
		Review:    ReviewNone,
		Artifacts: make(map[Stage]string),
		Timeline:  []TimelineEvent{},
	}
}

// Timeline 事件类型常量:前端据此渲染时间线节点(图标/颜色/文案)。
const (
	EventStageEntered     = "stage_entered"     // 阶段进入(advance_stage 成功)
	EventArtifactCreated  = "artifact_created"  // 产出物检测到
	EventReviewSubmitted  = "review_submitted"  // 提交人工审核
	EventReviewApproved   = "review_approved"   // 审核通过
	EventReviewRejected   = "review_rejected"   // 审核驳回
	EventSimulationPassed = "simulation_passed" // 对局模拟达标
	EventSimulationFailed = "simulation_failed" // 对局模拟未达标
)

// TimelineEvent 流程时间线事件:某阶段发生的动作及其时间戳。
type TimelineEvent struct {
	Stage  Stage     `json:"stage"`
	Event  string    `json:"event"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// maxTimelineEvents 时间线上限,防止长时间项目无限增长。
const maxTimelineEvents = 100

// RecordTimeline 追加一条时间线事件(截断到最近 maxTimelineEvents 条)。
func RecordTimeline(s *WorkflowState, stage Stage, event, detail string) {
	s.Timeline = append(s.Timeline, TimelineEvent{
		Stage:  stage,
		Event:  event,
		Detail: detail,
		At:     time.Now(),
	})
	if len(s.Timeline) > maxTimelineEvents {
		s.Timeline = s.Timeline[len(s.Timeline)-maxTimelineEvents:]
	}
}

// stageOrder 阶段顺序(advance_stage 只能沿此顺序前进)
var stageOrder = []Stage{
	StageInit,
	StageFlowAnalysis,
	StageModuleDesign,
	StageCodeGen,
	StageSelfTest,
	StageQACases,
	StageDone,
}

// NextStage 返回下一阶段;当前已是终态时返回空串。
func NextStage(s Stage) Stage {
	for i, st := range stageOrder {
		if st == s && i+1 < len(stageOrder) {
			return stageOrder[i+1]
		}
	}
	return ""
}

// PrevStage 返回上一阶段;初始阶段返回空串。
func PrevStage(s Stage) Stage {
	for i, st := range stageOrder {
		if st == s && i > 0 {
			return stageOrder[i-1]
		}
	}
	return ""
}

// IsBefore 判断 a 是否在 b 之前(不含相等)。
func IsBefore(a, b Stage) bool {
	ai, bi := -1, -1
	for i, st := range stageOrder {
		if st == a {
			ai = i
		}
		if st == b {
			bi = i
		}
	}
	return ai != -1 && bi != -1 && ai < bi
}

// WorkflowDef 流程定义(config.yaml workflow 段 YAML 化):不同项目可配置
// 不同的审核点与校验项,强制但不写死。
type WorkflowDef struct {
	// RequirePlan 为 true 时 init → flow_analysis 强制要求已读取策划案
	// (PlanLoaded,由 read_feishu_doc 置位)。
	RequirePlan bool `yaml:"require_plan"`

	// ReviewStages 是需要人工审核的阶段;当前仅支持 module_design
	// (唯一的审核点,审核状态只作用于该阶段)。
	ReviewStages []Stage `yaml:"review_stages"`

	// RequireBuild 为 true 时 code_gen → self_test 实跑 go build 校验。
	RequireBuild bool `yaml:"require_build"`

	// RequireTest 为 true 时 self_test → qa_cases 实跑 go test 校验。
	RequireTest bool `yaml:"require_test"`

	// RequireSimulation 为 true 时 self_test → qa_cases 要求对局模拟达标
	// (SimulationPassed,由 simulate_match 置位):棋牌游戏除单测外还需
	// 概率分布与金币流水守恒的蒙特卡洛验证。
	RequireSimulation bool `yaml:"require_simulation"`
}

// DefaultDef 返回默认流程定义(对齐 game.md 五阶段规范)。
func DefaultDef() WorkflowDef {
	return WorkflowDef{
		RequirePlan:       false,
		ReviewStages:      []Stage{StageModuleDesign},
		RequireBuild:      true,
		RequireTest:       true,
		RequireSimulation: true,
	}
}

// HasReview 判断阶段是否需要人工审核。
func (d WorkflowDef) HasReview(stage Stage) bool {
	for _, s := range d.ReviewStages {
		if s == stage {
			return true
		}
	}
	return false
}

// Validate 校验流程定义。当前仅 module_design 支持人工审核。
func (d WorkflowDef) Validate() error {
	for _, s := range d.ReviewStages {
		if s != StageModuleDesign {
			return fmt.Errorf("workflow.review_stages 当前仅支持 module_design(收到 %q)", s)
		}
	}
	return nil
}

// StageNode 是给前端渲染流程节点用的结构:Current 节点加粗显示,
// Done 节点置灰/打勾(见 game.md 前端契约)。
type StageNode struct {
	Name    Stage  `json:"name"`
	Desc    string `json:"desc"`
	Current bool   `json:"current"`
	Done    bool   `json:"done"`
}

// StageNodes 返回全部阶段节点及当前/已完成标记。
func StageNodes(state *WorkflowState) []StageNode {
	nodes := make([]StageNode, 0, len(stageOrder))
	for _, st := range stageOrder {
		nodes = append(nodes, StageNode{
			Name:    st,
			Desc:    StageDescriptions[st],
			Current: st == state.Stage,
			Done:    IsBefore(st, state.Stage),
		})
	}
	return nodes
}

// StageConstraint 是注入给 LLM 的阶段约束描述:当前阶段、允许与禁止事项。
func StageConstraint(s *WorkflowState) string {
	switch s.Stage {
	case StageInit:
		return "当前阶段 init:所有写操作被禁止。必须先调用 advance_stage 进入 flow_analysis(策划案由用户消息提供)。"
	case StageFlowAnalysis:
		return "当前阶段 flow_analysis:只允许写 docs/flow.md(docs/ 目录下)。产出后调用 advance_stage 推进。禁止写任何代码文件。"
	case StageModuleDesign:
		return "当前阶段 module_design:只允许写 docs/modules.md。产出后必须调用 request_review 提交人工审核,审核通过才能推进。禁止写任何代码文件。"
	case StageCodeGen:
		return "当前阶段 code_gen:允许写代码(除 docs/ 下的锁定文档)。编译通过后调用 advance_stage 推进。禁止修改 docs/flow.md 与 docs/modules.md。"
	case StageSelfTest:
		return "当前阶段 self_test:允许写测试代码与运行 go test。还必须生成 simulation/main.go 对局模拟器并调用 simulate_match 完成概率与金币守恒验证,达标后才能调用 advance_stage 推进。"
	case StageQACases:
		return "当前阶段 qa_cases:只允许写 docs/qa_cases.md。产出后调用 advance_stage 完成流程。"
	case StageDone:
		return "当前阶段 done:流程已完结,产出物全部就绪。"
	default:
		return ""
	}
}
