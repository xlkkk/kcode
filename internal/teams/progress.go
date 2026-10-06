// progress.go — 队友实时进度跟踪。
//
// 备注 internal/teams/progress.go（简化：去掉 TUI spinner 依赖）

package teams

import (
	"fmt"
	"sync"
	"time"
)

// ToolActivity describes a single tool invocation.
type ToolActivity struct {
	ToolName    string
	Description string
}

// NewToolActivity creates a ToolActivity with an auto-generated description.
func NewToolActivity(toolName string, input map[string]interface{}) ToolActivity {
	return ToolActivity{
		ToolName:    toolName,
		Description: describeActivity(toolName, input),
	}
}

// describeActivity 用 kcode 工具名（小写蛇形）生成人类可读的活动摘要。
func describeActivity(toolName string, input map[string]interface{}) string {
	getStr := func(key string) string {
		if v, ok := input[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	switch toolName {
	case "read_file":
		return "Reading " + getStr("file_path")
	case "edit_file":
		return "Editing " + getStr("file_path")
	case "write_file":
		return "Writing " + getStr("file_path")
	case "bash":
		cmd := getStr("command")
		if len(cmd) > 40 {
			cmd = cmd[:40] + "…"
		}
		return "Running " + cmd
	case "glob":
		return "Searching " + getStr("pattern")
	case "grep":
		return "Grepping " + getStr("pattern")
	default:
		return toolName
	}
}

// TeammateProgress tracks real-time progress for one teammate.
// All methods are goroutine-safe.
type TeammateProgress struct {
	mu               sync.Mutex
	Name             string
	TeamName         string
	Status           string // "running", "idle", "completed", "failed", "stopped"
	ToolUseCount     int
	TokenCount       int64
	LastActivity     *ToolActivity
	RecentActivities []ToolActivity // max 5
	StartTime        int64          // unix ms
	LastMessage      string
}

func NewTeammateProgress(name, teamName string) *TeammateProgress {
	return &TeammateProgress{
		Name:      name,
		TeamName:  teamName,
		Status:    "running",
		StartTime: time.Now().UnixMilli(),
	}
}

func (p *TeammateProgress) RecordToolUse(toolName string, input map[string]interface{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ToolUseCount++
	act := NewToolActivity(toolName, input)
	p.LastActivity = &act
	p.RecentActivities = append(p.RecentActivities, act)
	if len(p.RecentActivities) > 5 {
		p.RecentActivities = p.RecentActivities[1:]
	}
}

func (p *TeammateProgress) RecordTokens(input, output int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.TokenCount = input + output
}

func (p *TeammateProgress) SetStatus(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Status = s
}

func (p *TeammateProgress) GetStatus() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Status
}

func (p *TeammateProgress) SetLastMessage(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LastMessage = msg
}

func (p *TeammateProgress) ActivitySummary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.LastActivity != nil {
		return p.LastActivity.Description
	}
	return "Working"
}

func (p *TeammateProgress) GetToolUseCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ToolUseCount
}

func (p *TeammateProgress) GetTokenCount() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.TokenCount
}

// FormatTokens formats a token count for display.
func FormatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
