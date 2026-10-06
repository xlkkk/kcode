// messages.go — Web 端上下行消息协议。
//
// 对应移植计划 Phase 7.2。下行事件（Server → Client）与上行事件
// （Client → Server）都用 {type, data} 信封；data 各自反序列化成下面的
// 结构体。id 字段用于请求/应答配对（ask_user、permission_request）。

package web

import (
	"encoding/json"

	"kcode/internal/workflow"
)

// 下行事件类型
const (
	EvtStreamText        = "stream_text"
	EvtToolCall          = "tool_call"
	EvtToolResult        = "tool_result"
	EvtAskUser           = "ask_user"
	EvtPermissionRequest = "permission_request"
	EvtSubAgentProgress  = "sub_agent_progress"
	EvtTurnComplete      = "turn_complete"
	EvtError             = "error"
	EvtUserTextEcho      = "user_text" // 队友消息等旁路文本进前端
	EvtReviewRequest     = "review_request"
	EvtWorkflowTimeline  = "workflow_timeline" // 流程时间线(每轮结束后推送)
)

// 上行事件类型
const (
	EvtUserMessage      = "user_message"
	EvtAskUserResponse  = "ask_user_response"
	EvtPermissionResult = "permission_response"
	EvtAbort            = "abort"
	EvtReviewResponse   = "review_response"
)

// Envelope 是所有消息的信封。
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// --- 下行 data ---

type StreamTextData struct {
	Text string `json:"text"`
}

type ToolCallData struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type ToolResultData struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// AskUserData 是转发给前端的 AskUserQuestion 请求。Questions 直接
// 复用 tools.AskUserRequest.Questions 的 JSON 形状（question/header/options）。
type AskUserData struct {
	ID        string `json:"id"`
	Questions any    `json:"questions"`
}

type PermissionRequestData struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
}

type SubAgentProgressData struct {
	Name    string  `json:"name"`
	AgentID string  `json:"agent_id,omitempty"`
	Tool    string  `json:"tool,omitempty"`
	Elapsed float64 `json:"elapsed,omitempty"`
	IsError bool    `json:"is_error,omitempty"`
	Done    bool    `json:"done"`
}

type TurnCompleteData struct {
	Turn int `json:"turn,omitempty"`
}

// WorkflowTimelineData 是流程时间线快照:五阶段节点状态 + 事件时间线,
// 前端据此渲染横排流程节点与纵向时间线(阶段进入/产出物/审核/模拟事件)。
// 每轮结束后由服务端推送,与 review_request 面板数据互补。
type WorkflowTimelineData struct {
	Stage            string                   `json:"stage"`                       // 当前阶段
	Nodes            []workflow.StageNode     `json:"nodes"`                       // 阶段节点(Current 加粗/Done 打勾)
	Events           []workflow.TimelineEvent `json:"events"`                      // 时间线事件(按发生时间排序)
	Review           string                   `json:"review,omitempty"`            // 审核状态
	SimulationPassed bool                     `json:"simulation_passed,omitempty"` // 对局模拟是否达标
}

type ErrorData struct {
	Message string `json:"message"`
}

// --- 上行 data ---

type UserMessageData struct {
	Content string `json:"content"`
}

type AskUserResponseData struct {
	ID      string            `json:"id"`
	Answers map[string]string `json:"answers"`
}

type PermissionResponseData struct {
	ID       string `json:"id"`
	Response string `json:"response"` // "allow" | "deny"
}

// ReviewRequestData 是工作流人工审核请求(升级版审核 UI):携带审核对象
// 文档内容供前端预览,Stages 标记 Current/Done,前端对 Current 节点加粗
// 渲染流程进度(见 game.md 前端契约)。
type ReviewRequestData struct {
	ID              string               `json:"id"`
	ArtifactPath    string               `json:"artifact_path"`
	ArtifactContent string               `json:"artifact_content"`
	Stages          []workflow.StageNode `json:"stages"`
}

// ReviewResponseData 用户审核决策。
type ReviewResponseData struct {
	ID       string `json:"id"`
	Decision string `json:"decision"` // "approve" | "reject"
	Feedback string `json:"feedback,omitempty"`
}
