// consolidation.go — 后台记忆整理（autoDream）的 Eino 实现。
//
// 满足两个门控条件后自动触发：距上次整理超过 minHours 小时，
// 且期间累积了 minSessions 个以上会话。触发后 fork 一个子 Agent
// 在后台扫描现有记忆，合并重复、删除过时、修正矛盾、维护索引。
//
// 备注 internal/memory/consolidation/consolidation.go

package consolidation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kcode/internal/memory"
	"kcode/internal/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultMinHours    = 24
	defaultMinSessions = 5
	// scanThrottleMs 防止时间门通过但会话门未通过时每轮都扫会话目录
	scanThrottleMs = 10 * 60 * 1000
)

// Deps 是 Consolidator 的外部依赖。
type Deps struct {
	ChatModel     model.BaseModel[*schema.Message] // 子 Agent 用的 ChatModel
	SystemPrompt  string                           // 主 Agent 的 System Prompt
	MemoryDir     string                           // <wd>/.kcode/memory/
	UserMemoryDir string                           // ~/.kcode/memory/
	ProjectRoot   string                           // 项目根目录绝对路径
	ToolRegistry  *tools.Registry                  // 父 Agent 的工具注册表
	AppendSystem  func(string)                     // 通知交互层
	DebugLogf     func(string, ...any)             // 调试日志
}

// Consolidator 管理后台记忆整理的状态和执行。
type Consolidator struct {
	deps        Deps
	lastScanAt  int64 // 上次扫描会话目录的时间（ms）
	minHours    int
	minSessions int
}

// NewConsolidator 创建一个新的整理器。
func NewConsolidator(deps Deps) *Consolidator {
	return &Consolidator{
		deps:        deps,
		minHours:    defaultMinHours,
		minSessions: defaultMinSessions,
	}
}

// SetThresholds 用于测试：覆盖默认的门控阈值。
func (c *Consolidator) SetThresholds(minHours, minSessions int) {
	c.minHours = minHours
	c.minSessions = minSessions
}

// MaybeRun 检查门控条件，满足则异步执行一次后台整理。
// 每轮 Agent Loop 完成后调用，成本极低（一次 stat）。
func (c *Consolidator) MaybeRun(ctx context.Context) {
	if c == nil {
		return
	}
	// 记忆目录不存在则跳过
	if _, err := os.Stat(strings.TrimRight(c.deps.MemoryDir, string(filepath.Separator))); os.IsNotExist(err) {
		return
	}

	// 时间门：距上次整理是否超过阈值
	lastAt, err := ReadLastConsolidatedAt(c.deps.MemoryDir)
	if err != nil {
		c.debugf("[consolidation] ReadLastConsolidatedAt failed: %v", err)
		return
	}
	hoursSince := float64(time.Now().UnixMilli()-lastAt) / 3_600_000
	if hoursSince < float64(c.minHours) {
		return
	}

	// 扫描节流：防止每轮都扫会话目录
	now := time.Now().UnixMilli()
	if now-c.lastScanAt < scanThrottleMs {
		c.debugf("[consolidation] scan throttle — last scan %ds ago", (now-c.lastScanAt)/1000)
		return
	}
	c.lastScanAt = now

	// 会话门：累积的会话数是否达到阈值
	sessionIDs := listSessionsSince(c.deps.ProjectRoot, lastAt)
	if len(sessionIDs) < c.minSessions {
		c.debugf("[consolidation] skip — %d sessions since last consolidation, need %d",
			len(sessionIDs), c.minSessions)
		return
	}

	// 获取锁
	priorMtime, err := TryAcquireLock(c.deps.MemoryDir)
	if err != nil {
		c.debugf("[consolidation] lock acquire failed: %v", err)
		return
	}
	if priorMtime == -1 {
		c.debugf("[consolidation] lock held by another process")
		return
	}

	c.debugf("[consolidation] firing — %.1fh since last, %d sessions to review",
		hoursSince, len(sessionIDs))

	go c.run(ctx, sessionIDs, priorMtime)
}

// consolidationToolWhitelist 整理子 Agent 允许的工具（与提取器一致的受限集）。
var consolidationToolWhitelist = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
	"glob":       true,
	"grep":       true,
	"bash":       true,
}

func (c *Consolidator) run(ctx context.Context, sessionIDs []string, priorMtime int64) {
	defer func() {
		if r := recover(); r != nil {
			c.debugf("[consolidation] panic: %v", r)
			RollbackLock(c.deps.MemoryDir, priorMtime)
		}
	}()

	transcriptDir := filepath.Join(c.deps.ProjectRoot, ".kcode", "sessions")
	prompt := BuildConsolidationPrompt(
		c.deps.MemoryDir, c.deps.UserMemoryDir,
		transcriptDir, sessionIDs,
	)

	// 构建独立会话：不继承父 Agent 上下文，从空白开始
	messages := []*schema.Message{schema.UserMessage(prompt)}

	subTools := filterTools(ctx, c.deps.ToolRegistry.GetAll(), consolidationToolWhitelist)
	sandboxMW := memory.NewSubAgentMW(c.deps.MemoryDir, c.deps.UserMemoryDir)

	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "memory-consolidator",
		Model:         c.deps.ChatModel,
		Instruction:   c.deps.SystemPrompt,
		MaxIterations: 15, // 整理可能需要多轮读写
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: subTools,
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{sandboxMW},
	})
	if err != nil {
		c.debugf("[consolidation] build sub-agent failed: %v", err)
		RollbackLock(c.deps.MemoryDir, priorMtime)
		return
	}

	startTime := time.Now()
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: subAgent})
	iter := runner.Run(ctx, messages)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			c.debugf("[consolidation] sub-agent event error: %v", ev.Err)
		}
	}

	writtenPaths := sandboxMW.WrittenPaths()
	c.debugf("[consolidation] finished in %s, %d files touched: %v",
		time.Since(startTime), len(writtenPaths), writtenPaths)

	// 过滤掉索引文件，只通知实际的记忆文件修改
	var memoryPaths []string
	for _, p := range writtenPaths {
		if filepath.Base(p) == memory.AutoMemEntrypointName {
			continue
		}
		memoryPaths = append(memoryPaths, p)
	}

	if len(memoryPaths) > 0 && c.deps.AppendSystem != nil {
		var names []string
		for _, p := range memoryPaths {
			names = append(names, filepath.Base(p))
		}
		c.deps.AppendSystem(fmt.Sprintf("Memory improved: %s", strings.Join(names, ", ")))
	}
}

// listSessionsSince 返回 sinceMs 之后被修改过的会话 ID 列表。
// 直接扫 <root>/.kcode/sessions/ 下的 *.jsonl / *.json 文件。
func listSessionsSince(projectRoot string, sinceMs int64) []string {
	dir := filepath.Join(projectRoot, ".kcode", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	since := time.UnixMilli(sinceMs)
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(since) {
			ids = append(ids, strings.TrimSuffix(strings.TrimSuffix(name, ".jsonl"), ".json"))
		}
	}
	return ids
}

// filterTools 按白名单过滤工具（依据 Info() 返回的工具名）。
func filterTools(ctx context.Context, all []tool.BaseTool, allow map[string]bool) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(allow))
	for _, t := range all {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}
		if allow[info.Name] {
			out = append(out, t)
		}
	}
	return out
}

func (c *Consolidator) debugf(format string, args ...any) {
	if c.deps.DebugLogf != nil {
		c.deps.DebugLogf(format, args...)
	}
}
