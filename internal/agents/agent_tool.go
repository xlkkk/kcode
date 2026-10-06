// agent_tool.go — Agent 工具（Eino InvokableTool），子 Agent 系统的执行入口。
//
// 备注 internal/agents/agent_tool.go
// 差异：
//   - 子 Agent 用 adk.ChatModelAgent + adk.Runner 运行（不再走 conversation.Manager）
//   - 权限通过派生一份 headless PermissionMW 实现（Ask 自动拒绝）
//   - worktree 隔离 / teammate 派发以钩子预留，Phase 5/6 集成时注入，
//     避免 agents → worktree/teams 的编译依赖成环
//   - 去掉 plan 相关参数（plan_mode_required、mode 的 plan 值）

package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"kcode/internal/middleware"
	"kcode/internal/permissions"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// --- fork 相关常量 ---

const ForkBoilerplateTag = "<fork_boilerplate>"

// ForkAgentType 是 fork 子 Agent 的类型名，拼进 QuerySource 用来识别 fork 出来的会话。
const ForkAgentType = "fork"

// GeneralPurposeAgentType 是 fork 关闭时，省略 subagent_type 的回退目标。
const GeneralPurposeAgentType = "general-purpose"

// ForkQuerySource 是 fork 子 Agent 的来源标记。它是判断「当前身处 fork 子
// Agent」的首选信号 —— 压缩-resistant，即使 boilerplate 被摘要掉也能识别。
const ForkQuerySource = "agent:builtin:" + ForkAgentType

// SubAgentProgress 是子 Agent 运行期间的进度事件，发射给交互层（Web UI）展示。
type SubAgentProgress struct {
	AgentDesc string
	AgentType string
	ToolName  string
	ToolArgs  map[string]any
	Elapsed   float64
	IsError   bool
	Done      bool
	ToolCount int
	TotalTime float64
}

// WorktreeHooks 是 worktree 包的注入点（Phase 5 集成时装配）。
// 为 nil 时 isolation="worktree" 报错，其余路径不受影响。
type WorktreeHooks struct {
	// Create 创建一个隔离 worktree，返回路径、分支、git 根、起始 commit。
	Create func(ctx context.Context, slug string) (worktreePath, branch, gitRoot, headCommit string, err error)
	// HasChanges 检测 worktree 是否有未提交改动 / 新 commit。
	HasChanges func(ctx context.Context, worktreePath, headCommit string) bool
	// Remove 清理 worktree 与分支。
	Remove func(ctx context.Context, worktreePath, branch, gitRoot string) error
	// Notice 生成注入 prompt 的迁移提示。
	Notice func(parentCwd, worktreePath string) string
}

// TeammateSpawnRequest 是 teammate 派发钩子的入参（Phase 6 集成时装配）。
// Instruction / Handlers / MaxTurns 由 runAsTeammate 按 executeSubAgent
// 同样的规则预先解析好，teams 包拿它们直接拼 ChatModelAgent，不必了解
// 权限派生和 spec 覆盖逻辑。
type TeammateSpawnRequest struct {
	TeamName     string
	MemberName   string
	Description  string
	Prompt       string
	SubagentType string
	Isolation    string
	Spec         SubAgentSpec
	ChatModel    model.BaseModel[*schema.Message]
	Tools        []tool.BaseTool

	Instruction string
	Handlers    []adk.ChatModelAgentMiddleware
	MaxTurns    int
}

// AgentTool 实现 tool.InvokableTool：LLM 调用
// Agent({subagent_type, prompt, description}) 创建子 Agent。
type AgentTool struct {
	ChatModel     model.BaseModel[*schema.Message]                       // 父 Agent 的 ChatModel
	ModelResolver func(string) (model.BaseModel[*schema.Message], error) // 按模型名解析（可为 nil）
	AllTools      []tool.BaseTool                                        // 父 Agent 完整工具池
	SystemPrompt  string                                                 // 父 System Prompt（fork 完美继承；无覆盖时子 Agent 也用它）
	Loader        *AgentLoader                                           // 定义加载器（可为 nil → 只有 BuiltinSpecs）
	TaskMgr       *TaskManager                                           // 异步任务表
	ProgressCh    chan<- SubAgentProgress                                // 进度事件出口（可为 nil）

	// 权限资源：复用父的 Sandbox/RuleEngine，只覆盖 Mode。
	// 子 Agent 是 headless 的：派生出的 PermissionMW 不带 ConfirmFunc，
	// Ask 一律自动拒绝，不会挂在一个没人应答的确认上。
	Sandbox    *permissions.PathSandbox
	RuleEngine *permissions.RuleEngine
	PermMode   permissions.PermissionMode

	// ExtraHandlers 注入到每个子 Agent 的额外中间件（如 FileTrackMW，集成阶段装配）。
	ExtraHandlers []adk.ChatModelAgentMiddleware

	// WT / SpawnTeammate 是 Phase 5 / 6 的预留钩子。
	WT            *WorktreeHooks
	SpawnTeammate func(ctx context.Context, req TeammateSpawnRequest) (string, error)

	// QuerySource 标识派生方：主线程为空；fork 子 Agent 里的浅拷贝为
	// ForkQuerySource，再次 fork 会被拒。
	QuerySource string

	// ForkDisabled 为真时，省略 subagent_type 不再 fork，而是回退到通用 agent。
	// 用「关闭」而不是「开启」语义，让零值就是默认行为（fork 可用）。
	ForkDisabled bool

	// parentMessages 由 ParentStateMW 每轮同步，fork 路径消费。放在共享
	// 指针里：cloneToolsForFork 的浅拷贝与父实例共用同一份状态。
	fs *forkState
}

// forkState 封装 AgentTool 的可变运行时状态（浅拷贝安全）。
type forkState struct {
	mu             sync.Mutex
	parentMessages []*schema.Message
}

// NewAgentTool 构造 AgentTool。必须经由此构造函数创建，以保证
// 浅拷贝（fork 路径）后运行时状态仍与父实例共享。
func NewAgentTool() *AgentTool {
	return &AgentTool{fs: &forkState{}}
}

var _ tool.InvokableTool = &AgentTool{}

func (t *AgentTool) SetParentMessages(msgs []*schema.Message) {
	if t.fs == nil {
		return
	}
	t.fs.mu.Lock()
	t.fs.parentMessages = msgs
	t.fs.mu.Unlock()
}

func (t *AgentTool) getParentMessages() []*schema.Message {
	if t.fs == nil {
		return nil
	}
	t.fs.mu.Lock()
	defer t.fs.mu.Unlock()
	return t.fs.parentMessages
}

// --- tool.InvokableTool 接口 ---

func (t *AgentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	agentTypes := t.availableAgentTypes()

	return &schema.ToolInfo{
		Name: "Agent",
		Desc: t.description(),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"description": {
				Type:     schema.String,
				Desc:     "A short (3-5 word) description of the task",
				Required: true,
			},
			"prompt": {
				Type:     schema.String,
				Desc:     "The task for the agent to perform. Be detailed — the agent has no context from this conversation.",
				Required: true,
			},
			"subagent_type": {
				Type:     schema.String,
				Desc:     "The type of agent to use. If omitted, forks current conversation context.",
				Enum:     agentTypes,
				ElemInfo: nil,
				Required: false,
			},
			"model": {
				Type:     schema.String,
				Desc:     "Override the model for this agent.",
				Required: false,
			},
			"run_in_background": {
				Type:     schema.Boolean,
				Desc:     "Set to true to run the agent in the background.",
				Required: false,
			},
			"name": {
				Type:     schema.String,
				Desc:     "Name for the agent, enabling SendMessage communication.",
				Required: false,
			},
			"isolation": {
				Type:     schema.String,
				Desc:     "Isolation mode. Set to 'worktree' to give the agent its own git worktree so its file edits don't collide with peers or the lead. REQUIRED when spawning two or more teammates in parallel that may write files.",
				Enum:     []string{"worktree"},
				Required: false,
			},
			"team_name": {
				Type:     schema.String,
				Desc:     "Spawns the agent as a long-running teammate under this team (created via TeamCreate). Team members persist after the lead returns and communicate via SendMessage.",
				Required: false,
			},
			"mode": {
				Type:     schema.String,
				Desc:     "Permission mode override for the spawned agent.",
				Enum:     []string{"default", "acceptEdits", "bypassPermissions"},
				Required: false,
			},
		}),
	}, nil
}

func (t *AgentTool) description() string {
	desc := `Launch a sub-agent to handle a complex task. Each sub-agent runs independently with its own context. The sub-agent cannot see the current conversation.

This is ONE tool with multiple roles. Roles are NOT separate tools — you pick one by passing its name in the "subagent_type" parameter. Do not search for a tool named after a role; call THIS tool ("Agent") and set "subagent_type".

Available roles for the "subagent_type" parameter:`

	if t.Loader != nil {
		for _, name := range t.Loader.ListNames() {
			def := t.Loader.Get(name)
			if def != nil {
				desc += "\n- " + name + ": " + def.WhenToUse
			}
		}
	} else {
		desc += "\n- general-purpose: Full tool access for multi-step tasks (default)"
		desc += "\n- explore: Read-only search agent for locating code"
	}

	desc += `

Example call shape:
{
  "name": "Agent",
  "input": {
    "subagent_type": "<role from the list above>",
    "description": "Short task label",
    "prompt": "Detailed instructions — the sub-agent has zero prior context"
  }
}

Write a detailed prompt explaining what the sub-agent should do and why — it has no prior context.
When tasks are independent, launch multiple sub-agents in parallel by making multiple Agent tool calls in a single response.`
	return desc
}

// agentToolArgs 是 Agent 工具的参数结构（InvokableRun / runAsTeammate 共用）。
type agentToolArgs struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type"`
	Model           string `json:"model"`
	RunInBackground bool   `json:"run_in_background"`
	Name            string `json:"name"`
	Isolation       string `json:"isolation"`
	TeamName        string `json:"team_name"`
	Mode            string `json:"mode"`
}

// availableAgentTypes 返回 subagent_type 的合法取值列表。
func (t *AgentTool) availableAgentTypes() []string {
	if t.Loader != nil {
		return t.Loader.ListNames()
	}
	return listBuiltinNames()
}

func (t *AgentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args agentToolArgs
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Description == "" || args.Prompt == "" {
		return "", fmt.Errorf("description and prompt are required")
	}

	if args.Mode != "" && !validPermissionModes[args.Mode] {
		return fmt.Sprintf("Error: invalid mode '%s'. Valid: default, acceptEdits, bypassPermissions", args.Mode), nil
	}

	// Team-member path：显式 team_name 把这次派发生成一个长驻队友。
	if args.TeamName != "" && t.SpawnTeammate != nil {
		return t.runAsTeammate(ctx, args)
	}

	// 省略 subagent_type 时的走向由配置决定：fork 开着就继承父对话，关着就
	// 回退到通用 agent。模型只是没填一个可选参数，为此中断一次调用不值得。
	subagentType := args.SubagentType
	if subagentType == "" && t.ForkDisabled {
		subagentType = GeneralPurposeAgentType
	}

	// Fork path：没有 subagent_type，继承父对话上下文后台运行。
	if subagentType == "" {
		return t.runFork(ctx, args.Description, args.Prompt, args.Model, args.Name), nil
	}

	// Definition path：从加载器或内置 specs 解析。
	var spec SubAgentSpec
	if t.Loader != nil {
		def := t.Loader.Get(subagentType)
		if def == nil {
			return fmt.Sprintf("Error: unknown agent type '%s'. Available: %s",
				subagentType, strings.Join(t.Loader.ListNames(), ", ")), nil
		}
		spec = def.ToSpec()
	} else {
		s, ok := BuiltinSpecs[subagentType]
		if !ok {
			return fmt.Sprintf("Error: unknown agent type '%s'. Available: %s",
				subagentType, strings.Join(listBuiltinNames(), ", ")), nil
		}
		spec = s
	}

	// 每次调用的 mode 覆盖优先于定义的 permissionMode。
	if args.Mode != "" {
		spec.PermissionMode = args.Mode
	}

	// 调用方传 run_in_background，或者定义里标了 background，都走异步派发。
	if args.RunInBackground || spec.Background {
		return t.runAsync(ctx, spec, args.Description, args.Prompt, args.Model), nil
	}
	return t.runSync(ctx, spec, args.Description, args.Prompt, args.Model, args.Isolation)
}

// --- 三条执行路径 ---

func (t *AgentTool) runSync(ctx context.Context, spec SubAgentSpec, description, prompt, modelOverride, isolation string) (string, error) {
	subTools := FilterToolsForAgent(ctx, t.AllTools, spec.Tools, spec.DisallowedTools, false, false)
	chatModel := t.selectClient(spec.Model, modelOverride)

	// Worktree 隔离（钩子未注入时报错提示）。
	var wt *worktreeState
	if isolation == "worktree" || spec.Isolation == IsolationWorktree {
		if t.WT == nil || t.WT.Create == nil {
			return "Error: worktree isolation is not available in this build", nil
		}
		slug := generateAgentSlug(description)
		path, branch, gitRoot, headCommit, err := t.WT.Create(ctx, slug)
		if err != nil {
			return fmt.Sprintf("Error creating agent worktree: %s", err), nil
		}
		wt = &worktreeState{Path: path, Branch: branch, GitRoot: gitRoot, HeadCommit: headCommit}
		parentCwd, _ := os.Getwd()
		prompt = t.WT.Notice(parentCwd, path) + "\n\n" + prompt
	}

	if spec.InitialPrompt != "" {
		prompt = spec.InitialPrompt + "\n\n" + prompt
	}

	start := time.Now()
	output, toolCount, err := t.executeSubAgent(ctx, chatModel, subTools, spec, description,
		[]*schema.Message{schema.UserMessage(prompt)})
	elapsed := time.Since(start)

	emitProgress(t.ProgressCh, ctx, SubAgentProgress{
		AgentDesc: description,
		AgentType: spec.Name,
		Done:      true,
		ToolCount: toolCount,
		TotalTime: elapsed.Seconds(),
		IsError:   err != nil,
	})
	if err != nil {
		return fmt.Sprintf("Agent failed: %s", err), nil
	}

	result := output
	if result == "" {
		result = "(agent produced no output)"
	}

	// Worktree 清理：干净就删，有改动保留现场。
	if wt != nil {
		if t.WT.HasChanges(ctx, wt.Path, wt.HeadCommit) {
			result += fmt.Sprintf("\n\nWorktree kept at %s (branch %s) — has uncommitted changes or new commits.",
				wt.Path, wt.Branch)
		} else {
			t.WT.Remove(ctx, wt.Path, wt.Branch, wt.GitRoot)
		}
	}

	return fmt.Sprintf("Agent \"%s\" completed in %s.\n\n%s", description, elapsed.Round(time.Millisecond), result), nil
}

func (t *AgentTool) runAsync(ctx context.Context, spec SubAgentSpec, description, prompt, modelOverride string) string {
	subTools := FilterToolsForAgent(ctx, t.AllTools, spec.Tools, spec.DisallowedTools, true, false)
	chatModel := t.selectClient(spec.Model, modelOverride)

	taskID := t.TaskMgr.CreateTask(spec.Name + ": " + truncate(prompt, 50))
	subCtx, cancel := context.WithCancel(ctx)
	t.TaskMgr.SetRunning(taskID, cancel)

	if spec.InitialPrompt != "" {
		prompt = spec.InitialPrompt + "\n\n" + prompt
	}

	go func() {
		output, _, err := t.executeSubAgent(subCtx, chatModel, subTools, spec, description,
			[]*schema.Message{schema.UserMessage(prompt)})
		if err != nil {
			t.TaskMgr.SetFailed(taskID, err.Error())
			return
		}
		if output == "" {
			output = "(agent produced no output)"
		}
		t.TaskMgr.SetCompleted(taskID, output)
	}()

	return fmt.Sprintf("Agent \"%s\" launched in background (task %s). You will be notified when it completes.",
		description, taskID)
}

func (t *AgentTool) runFork(ctx context.Context, description, prompt, modelOverride, agentName string) string {
	parentMessages := t.getParentMessages()
	if len(parentMessages) == 0 {
		return "Error: fork requires parent conversation context (not captured yet)"
	}

	// 嵌套 fork 防护，两层：
	// (1) 主检查：QuerySource —— fork 子 Agent 里的浅拷贝 AgentTool 被赋值，
	//     压缩-resistant。
	// (2) 兜底：消息里扫 ForkBoilerplateTag。
	if t.QuerySource == ForkQuerySource {
		return "Error: cannot fork from a forked agent. Use subagent_type to spawn a definition-based agent instead."
	}
	for _, msg := range parentMessages {
		if strings.Contains(msg.Content, ForkBoilerplateTag) {
			return "Error: cannot fork from a forked agent. Use subagent_type to spawn a definition-based agent instead."
		}
	}

	forked := buildForkedMessages(parentMessages, prompt)
	chatModel := t.selectClient("", modelOverride)

	// fork 原样继承父工具池（请求前缀逐字节一致，prompt 缓存才命中得上），
	// 其中的 Agent 工具换成 QuerySource=ForkQuerySource 的浅拷贝，再往下
	// fork 会在入口检查处被拒。
	subTools := cloneToolsForFork(t.AllTools, t)

	taskName := "fork"
	if agentName != "" {
		taskName = agentName
	}
	taskID := t.TaskMgr.CreateTask(taskName + ": " + truncate(prompt, 50))
	forkCtx, cancel := context.WithCancel(ctx)
	t.TaskMgr.SetRunning(taskID, cancel)

	// fork 总是后台运行，权限状态原样继承父模式（headless：Ask 自动拒绝）。
	spec := SubAgentSpec{Name: ForkAgentType, MaxTurns: 200}

	go func() {
		output, _, err := t.executeSubAgent(forkCtx, chatModel, subTools, spec, description, forked)
		if err != nil {
			t.TaskMgr.SetFailed(taskID, err.Error())
			return
		}
		if output == "" {
			output = "(agent produced no output)"
		}
		t.TaskMgr.SetCompleted(taskID, output)
	}()

	return fmt.Sprintf("Forked agent \"%s\" launched in background (task %s). Results will arrive via task-notification.",
		description, taskID)
}

func (t *AgentTool) runAsTeammate(ctx context.Context, args agentToolArgs) (string, error) {
	memberName := args.Name
	if memberName == "" {
		memberName = sanitizeSlugSegment(args.Description)
	}

	var spec SubAgentSpec
	if args.SubagentType != "" {
		if t.Loader != nil {
			if def := t.Loader.Get(args.SubagentType); def != nil {
				spec = def.ToSpec()
			}
		} else if s, ok := BuiltinSpecs[args.SubagentType]; ok {
			spec = s
		}
	}

	teammateDisallowed := append(append([]string{}, spec.DisallowedTools...), TeammateDisallowedTools...)
	subTools := FilterToolsForAgent(ctx, t.AllTools, spec.Tools, teammateDisallowed, true, true)

	// 指令与中间件按 executeSubAgent 的规则组装：teams 包只拿到成品。
	instruction := t.SystemPrompt
	if spec.SystemPromptOverride != "" {
		instruction = spec.SystemPromptOverride
	}
	handlers := make([]adk.ChatModelAgentMiddleware, 0, 4)
	if permMW := t.derivePerm(spec); permMW != nil {
		handlers = append(handlers, permMW)
	}
	handlers = append(handlers, newSubProgressMW(t.ProgressCh, args.Description, spec.Name))
	handlers = append(handlers, t.ExtraHandlers...)

	out, err := t.SpawnTeammate(ctx, TeammateSpawnRequest{
		TeamName:     args.TeamName,
		MemberName:   memberName,
		Description:  args.Description,
		Prompt:       args.Prompt,
		SubagentType: args.SubagentType,
		Isolation:    args.Isolation,
		Spec:         spec,
		ChatModel:    t.selectClient(spec.Model, args.Model),
		Tools:        subTools,
		Instruction:  instruction,
		Handlers:     handlers,
		MaxTurns:     spec.MaxTurns,
	})
	if err != nil {
		return fmt.Sprintf("Error spawning teammate: %v", err), nil
	}
	return out, nil
}

// --- 子 Agent 构建与运行 ---

type worktreeState struct {
	Path       string
	Branch     string
	GitRoot    string
	HeadCommit string
}

// executeSubAgent 构建并运行一个子 Agent，收集其全部 assistant 文本。
// 同步路径等它跑完；异步/fork 路径在自己的 goroutine 里调用。
func (t *AgentTool) executeSubAgent(
	ctx context.Context,
	chatModel model.BaseModel[*schema.Message],
	subTools []tool.BaseTool,
	spec SubAgentSpec,
	description string,
	input []*schema.Message,
) (string, int, error) {
	instruction := t.SystemPrompt
	if spec.SystemPromptOverride != "" {
		instruction = spec.SystemPromptOverride
	}
	maxIter := spec.MaxTurns
	if maxIter <= 0 {
		maxIter = 200
	}

	handlers := make([]adk.ChatModelAgentMiddleware, 0, 4)
	if permMW := t.derivePerm(spec); permMW != nil {
		handlers = append(handlers, permMW)
	}
	handlers = append(handlers, newSubProgressMW(t.ProgressCh, description, spec.Name))
	handlers = append(handlers, t.ExtraHandlers...)

	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "subagent:" + spec.Name,
		Model:         chatModel,
		Instruction:   instruction,
		MaxIterations: maxIter,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: subTools,
			},
		},
		Handlers: handlers,
	})
	if err != nil {
		return "", 0, err
	}

	msgs := input

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: subAgent})
	iter := runner.Run(ctx, msgs)

	var output strings.Builder
	toolCount := 0
	var firstErr error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil && firstErr == nil {
			firstErr = ev.Err
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput
		if mv.Role != schema.Assistant {
			continue
		}
		// assistant 文本进输出；流式事件必须消费完，否则生产端阻塞。
		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				chunk, err := mv.MessageStream.Recv()
				if err != nil {
					break
				}
				output.WriteString(chunk.Content)
			}
		} else if mv.Message != nil {
			output.WriteString(mv.Message.Content)
			toolCount += len(mv.Message.ToolCalls)
		}
	}
	return output.String(), toolCount, firstErr
}

// derivePerm 为子 Agent 派生一份权限中间件：Sandbox / RuleEngine 与父共享，
// 只替换 Mode。子 Agent headless，不带 ConfirmFunc —— Ask 自动拒绝。
func (t *AgentTool) derivePerm(spec SubAgentSpec) adk.ChatModelAgentMiddleware {
	if t.Sandbox == nil || t.RuleEngine == nil {
		return nil
	}
	mode := t.PermMode
	if spec.PermissionMode != "" {
		mode = permissions.PermissionMode(spec.PermissionMode)
	}
	return middleware.NewPermissionMW(t.Sandbox, t.RuleEngine, mode)
}

// selectClient 按 spec.Model / 调用覆盖名解析模型，解析失败回退父 ChatModel。
func (t *AgentTool) selectClient(specModel, overrideModel string) model.BaseModel[*schema.Message] {
	name := overrideModel
	if name == "" {
		name = specModel
	}
	if name == "" || name == "inherit" {
		return t.ChatModel
	}
	if t.ModelResolver != nil {
		if m, err := t.ModelResolver(name); err == nil && m != nil {
			return m
		}
	}
	return t.ChatModel
}

// --- 辅助函数 ---

// emitProgress 非阻塞发送进度事件。消费方落后时丢弃事件 —— 进度是尽力而为
// 的 UI 反馈，不是承重状态。阻塞发送曾让子 Agent 循环在 ProgressCh 缓冲满
// 时死锁，连带 ESC / ctx 取消都到不了。
func emitProgress(ch chan<- SubAgentProgress, ctx context.Context, p SubAgentProgress) {
	if ch == nil {
		return
	}
	select {
	case ch <- p:
	case <-ctx.Done():
	default:
	}
}

// buildForkedMessages 复制父对话的纯文本消息（跳过 system 与悬空工具调用），
// 再追加 fork boilerplate + 任务。
func buildForkedMessages(parent []*schema.Message, task string) []*schema.Message {
	forked := make([]*schema.Message, 0, len(parent)+1)
	for _, m := range parent {
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
	forked = append(forked, schema.UserMessage(forkBoilerplate+"\n\nYour task:\n"+task))
	return forked
}

const forkBoilerplate = ForkBoilerplateTag + `
You are a forked worker process. You are NOT the main agent.
Rules (non-negotiable):
1. Do NOT fork again.
2. Do NOT converse, ask questions, or request confirmation.
3. Use tools directly: read files, search code, make changes.
4. Stay strictly within your assigned task scope.
5. Final report must be under 500 characters, starting with "Scope:".
` + "</fork_boilerplate>"

// sanitizeSlugSegment 把 [a-zA-Z0-9._-] 之外的字符换成 '-'，结果可安全
// 用作 git 分支名片段。
var unsafeSlugChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeSlugSegment(s string) string {
	clean := unsafeSlugChars.ReplaceAllString(s, "-")
	clean = strings.Trim(clean, "-_.")
	if clean == "" {
		clean = "subagent"
	}
	if len(clean) > 40 {
		clean = clean[:40]
	}
	return clean
}

// generateAgentSlug 生成 ^agent-a[0-9a-f]{7}$ 形态的 worktree 分支 slug。
func generateAgentSlug(description string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "agent-a" + hex.EncodeToString(b)[:7]
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func listBuiltinNames() []string {
	names := make([]string, 0, len(BuiltinSpecs))
	for name := range BuiltinSpecs {
		names = append(names, name)
	}
	return names
}
