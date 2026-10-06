package agent

import "time"

// AgentEvent 是 Agent 运行时产生的事件，供 TUI / Print / Remote 消费。
// 通过 Eino Runner 的 AsyncIterator[AgentEvent] 获取。
type AgentEvent interface{ agentEvent() }

type StreamText struct{ Text string }
type ThinkingText struct{ Text string }
type ToolUseEvent struct {
	ToolID   string
	ToolName string
	Args     map[string]any
}
type ToolResultEvent struct {
	ToolID   string
	ToolName string
	Output   string
	IsError  bool
	Elapsed  time.Duration
}
type TurnComplete struct{ Turn int }
type LoopComplete struct{ TotalTurns int }
type UsageEvent struct{ InputTokens, OutputTokens int }
type ErrorEvent struct{ Message string }
type CompactEvent struct{ Message string }
type RetryEvent struct {
	Reason string
	Wait   time.Duration
}

func (StreamText) agentEvent()      {}
func (ThinkingText) agentEvent()    {}
func (ToolUseEvent) agentEvent()    {}
func (ToolResultEvent) agentEvent() {}
func (TurnComplete) agentEvent()    {}
func (LoopComplete) agentEvent()    {}
func (UsageEvent) agentEvent()      {}
func (ErrorEvent) agentEvent()      {}
func (CompactEvent) agentEvent()    {}
func (RetryEvent) agentEvent()      {}
