// extractor.go — 后台记忆提取器的 Eino 实现。
//
// 触发方式：每轮对话结束后（AfterAgent / turn 完成回调）由调用方异步
// go Execute(ctx, messages)。Extractor 内部做在途合并与节流，fork 一个
// 受限工具集的子 Agent（adk.ChatModelAgent + Runner）在后台写记忆文件。
//
// 备注 internal/memory/extractor/extractor.go
package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kcode/internal/memory"
	"kcode/internal/tools"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Deps 是 Extractor 的外部依赖，启动时构造一次。
type Deps struct {
	ChatModel     model.BaseModel[*schema.Message] // 子 Agent 用的 ChatModel（与主 Agent 同款）
	SystemPrompt  string                           // 主 Agent 的 System Prompt（完美 fork）
	MemoryDir     string                           // <wd>/.kcode/memory/ — project/reference（带尾分隔符）
	UserMemoryDir string                           // ~/.kcode/memory/ — user/feedback（可为 ""）
	ProjectRoot   string                           // 项目绝对根目录
	ToolRegistry  *tools.Registry                  // 父工具注册表（按白名单过滤）
	AppendSystem  func(string)                     // 可选：向交互层通知 "Memory saved: ..."
	DebugLogf     func(format string, args ...any) // 可选：调试日志
}

// Extractor 后台记忆提取器。状态封装在结构体内，mu 保护全部可变字段。
type Extractor struct {
	deps Deps

	mu                       sync.Mutex
	inFlight                 map[*sync.WaitGroup]struct{}
	lastMemoryMessageIdx     int
	inProgress               bool
	turnsSinceLastExtraction int
	pendingContext           *pendingExtractionCtx
}

// pendingExtractionCtx 尾部提取的占位标记：非 nil 表示"当前跑完后再来一次"。
type pendingExtractionCtx struct{}

// InitExtractMemories 构造 Extractor。
func InitExtractMemories(deps Deps) *Extractor {
	return &Extractor{
		deps:     deps,
		inFlight: make(map[*sync.WaitGroup]struct{}),
	}
}

// Execute 是 fire-and-forget 入口，调用方以 `go e.Execute(ctx, msgs)` 触发。
// 错误一律吞掉（尽力而为），返回值仅供测试断言。
func (e *Extractor) Execute(ctx context.Context, messages []*schema.Message) error {
	if e == nil {
		return nil
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	e.mu.Lock()
	e.inFlight[wg] = struct{}{}
	e.mu.Unlock()
	defer func() {
		wg.Done()
		e.mu.Lock()
		delete(e.inFlight, wg)
		e.mu.Unlock()
	}()

	// 在途合并：已有提取在跑时，把这次调用记为尾部运行，立即返回。
	e.mu.Lock()
	if e.inProgress {
		e.deps.debugf("[extractMemories] extraction in progress — stashing for trailing run")
		e.pendingContext = &pendingExtractionCtx{}
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	return e.runExtraction(ctx, messages, false)
}

// extractionToolWhitelist 子 Agent 允许使用的工具（其余全部过滤掉）。
var extractionToolWhitelist = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
	"glob":       true,
	"grep":       true,
	"bash":       true,
}

func (e *Extractor) runExtraction(ctx context.Context, messages []*schema.Message, isTrailingRun bool) error {
	newMessageCount := countModelVisibleMessagesSince(messages, e.lastMessageIdx())

	// 互斥：主 Agent 本轮已自己写过记忆文件时，fork 出的提取器就是多余的。
	if hasMemoryWritesSince(messages, e.lastMessageIdx(), e.deps.ProjectRoot) {
		e.deps.debugf("[extractMemories] skipping — conversation already wrote to memory files")
		e.advanceCursor(len(messages))
		return nil
	}

	// 节流：默认为 1（每轮都跑）。尾部运行跳过节流，因为它处理的是已提交的工作。
	if !isTrailingRun {
		e.mu.Lock()
		e.turnsSinceLastExtraction++
		turns := e.turnsSinceLastExtraction
		e.turnsSinceLastExtraction = 0
		e.mu.Unlock()
		if turns < 1 {
			return nil
		}
	}

	e.mu.Lock()
	e.inProgress = true
	e.mu.Unlock()
	startTime := time.Now()

	defer func() {
		e.mu.Lock()
		e.inProgress = false
		trailing := e.pendingContext
		e.pendingContext = nil
		e.mu.Unlock()
		if trailing != nil {
			e.deps.debugf("[extractMemories] running trailing extraction for stashed context")
			_ = e.runExtraction(ctx, messages, true)
		}
	}()

	e.deps.debugf("[extractMemories] starting — %d new messages, memoryDir=%s, userMemoryDir=%s",
		newMessageCount, e.deps.MemoryDir, e.deps.UserMemoryDir)

	// 预注入记忆目录清单，省掉子 Agent 一轮 `ls`。
	var combinedScan []memory.MemoryHeader
	if e.deps.UserMemoryDir != "" {
		userScan, _ := memory.ScanMemoryFiles(ctx, strings.TrimSuffix(e.deps.UserMemoryDir, string(filepath.Separator)), "user")
		combinedScan = append(combinedScan, userScan...)
	}
	projectScan, _ := memory.ScanMemoryFiles(ctx, strings.TrimSuffix(e.deps.MemoryDir, string(filepath.Separator)), "project")
	combinedScan = append(combinedScan, projectScan...)
	manifest := memory.FormatMemoryManifest(combinedScan)
	extractionPrompt := BuildExtractAutoOnlyPrompt(newMessageCount, manifest, false, e.deps.UserMemoryDir, e.deps.MemoryDir)

	// 构建 fork 消息：复制主对话的纯文本消息 + 追加提取指令。
	forked := buildForkedMessages(messages, extractionPrompt)

	// 过滤工具白名单。
	subTools := filterTools(ctx, e.deps.ToolRegistry.GetAll(), extractionToolWhitelist)

	// 沙箱中间件：写操作只允许落在两个记忆目录内，bash 只允许只读命令。
	sandboxMW := memory.NewSubAgentMW(e.deps.MemoryDir, e.deps.UserMemoryDir)

	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "memory-extractor",
		Model:         e.deps.ChatModel,
		Instruction:   e.deps.SystemPrompt, // 完美 fork：与主 Agent 同一 System Prompt
		MaxIterations: 5,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: subTools,
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{sandboxMW},
	})
	if err != nil {
		return err
	}

	// 跑到结束，只 drain 事件流 —— 子 Agent 的流式文本不上屏，只有文件写入有意义。
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: subAgent})
	iter := runner.Run(ctx, forked)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			e.deps.debugf("[extractMemories] sub-agent event error: %v", ev.Err)
		}
	}

	// 无论有没有写文件都推进游标 —— "跑过但没挑中"的一轮不应该被重新考虑。
	e.advanceCursor(len(messages))

	writtenPaths := sandboxMW.WrittenPaths()
	e.deps.debugf("[extractMemories] finished in %s, %d files written: %v",
		time.Since(startTime), len(writtenPaths), writtenPaths)

	// MEMORY.md 索引是机械产物 —— 用户可见的"记忆"是主题文件而不是索引更新。
	var memoryPaths []string
	for _, p := range writtenPaths {
		if filepath.Base(p) == memory.AutoMemEntrypointName {
			continue
		}
		memoryPaths = append(memoryPaths, p)
	}

	if len(memoryPaths) > 0 && e.deps.AppendSystem != nil {
		var names []string
		for _, p := range memoryPaths {
			names = append(names, filepath.Base(p))
		}
		e.deps.AppendSystem(fmt.Sprintf("Memory saved: %s", strings.Join(names, ", ")))
	}

	return nil
}

// Drain 等待所有在途提取（含尾部运行）结束，带软超时。
// 退出路径调用，避免 fork 出的子 Agent 写文件写到一半被杀。
// timeoutMs 为负数时按 60000（60s）处理。
func (e *Extractor) Drain(timeoutMs int) error {
	if e == nil {
		return nil
	}
	if timeoutMs < 0 {
		timeoutMs = 60000
	}

	e.mu.Lock()
	wgs := make([]*sync.WaitGroup, 0, len(e.inFlight))
	for wg := range e.inFlight {
		wgs = append(wgs, wg)
	}
	e.mu.Unlock()

	if len(wgs) == 0 {
		return nil
	}

	done := make(chan struct{})
	go func() {
		for _, wg := range wgs {
			wg.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return nil
	}
}

func (e *Extractor) lastMessageIdx() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastMemoryMessageIdx
}

func (e *Extractor) advanceCursor(to int) {
	e.mu.Lock()
	if to > e.lastMemoryMessageIdx {
		e.lastMemoryMessageIdx = to
	}
	e.mu.Unlock()
}

// countModelVisibleMessagesSince 统计 sinceIdx 之后 user/assistant 消息数量。
// sinceIdx 越界（如上下文被压缩、游标不再对应任何位置）时退化为全量统计。
func countModelVisibleMessagesSince(messages []*schema.Message, sinceIdx int) int {
	if sinceIdx < 0 || sinceIdx > len(messages) {
		return countModelVisible(messages)
	}
	n := 0
	for _, m := range messages[sinceIdx:] {
		if isModelVisibleMessage(m) {
			n++
		}
	}
	return n
}

func countModelVisible(messages []*schema.Message) int {
	n := 0
	for _, m := range messages {
		if isModelVisibleMessage(m) {
			n++
		}
	}
	return n
}

func isModelVisibleMessage(m *schema.Message) bool {
	return m.Role == schema.User || m.Role == schema.Assistant
}

// hasMemoryWritesSince 返回 sinceIdx 之后的 assistant 消息里是否有针对
// 记忆路径的 write_file / edit_file 工具调用。
func hasMemoryWritesSince(messages []*schema.Message, sinceIdx int, projectRoot string) bool {
	if sinceIdx < 0 {
		sinceIdx = 0
	}
	if sinceIdx >= len(messages) {
		return false
	}
	for _, m := range messages[sinceIdx:] {
		if m.Role != schema.Assistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			fp := getWrittenFilePath(tc)
			if fp == "" {
				continue
			}
			if memory.IsAutoMemPath(fp, projectRoot) {
				return true
			}
		}
	}
	return false
}

// getWrittenFilePath 从 write_file / edit_file 工具调用中提取 path 参数。
func getWrittenFilePath(tc schema.ToolCall) string {
	if tc.Function.Name != "write_file" && tc.Function.Name != "edit_file" {
		return ""
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return ""
	}
	return args.Path
}

// buildForkedMessages 复制主对话的纯文本消息（跳过 system 与带工具调用的
// 消息，避免出现悬空的 tool_calls / tool results），再追加提取指令。
func buildForkedMessages(messages []*schema.Message, prompt string) []*schema.Message {
	forked := make([]*schema.Message, 0, len(messages)+1)
	for _, m := range messages {
		switch m.Role {
		case schema.User:
			if len(m.ToolCalls) > 0 || m.Content == "" {
				continue
			}
			forked = append(forked, schema.UserMessage(m.Content))
		case schema.Assistant:
			if len(m.ToolCalls) > 0 || m.Content == "" {
				continue
			}
			forked = append(forked, schema.AssistantMessage(m.Content, nil))
		}
	}
	forked = append(forked, schema.UserMessage(prompt))
	return forked
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

func (d Deps) debugf(format string, args ...any) {
	if d.DebugLogf != nil {
		d.DebugLogf(format, args...)
	}
}
