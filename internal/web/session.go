// session.go — 一条 WebSocket 连接对应一个 Agent Session。
//
// 对应移植计划 Phase 7.3：
//   - 懒组装完整 Agent（tools/middleware/permission/teams/worktree/filehistory）；
//   - 桥接 AskUserTool 的 RequestCh ↔ ask_user / ask_user_response 事件；
//   - 桥接 PermissionMW.ConfirmFunc ↔ permission_request / permission_response；
//   - 转发子 Agent 进度事件；abort 取消当前轮；
//   - 每轮结束异步触发记忆提取（extractor）与整理门控（consolidator）。

package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"kcode/internal/agent"
	"kcode/internal/agents"
	"kcode/internal/compact"
	"kcode/internal/config"
	"kcode/internal/feishu"
	"kcode/internal/filehistory"
	"kcode/internal/llm"
	"kcode/internal/mcp"
	"kcode/internal/memory/consolidation"
	"kcode/internal/memory/extractor"
	"kcode/internal/middleware"
	"kcode/internal/permissions"
	"kcode/internal/prompt"
	"kcode/internal/skills"
	"kcode/internal/teams"
	"kcode/internal/todo"
	"kcode/internal/tools"
	"kcode/internal/workflow"
	"kcode/internal/worktree"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const baseSystemPrompt = `You are kcode, an interactive AI coding agent. You help users write, understand, and refactor code by taking action rather than just talking about it.

Rules:
- Prefer doing over describing: use the tools to read, edit, and run.
- Keep responses short and high-signal; code and tool results carry the detail.
- When instructions are ambiguous, use AskUserQuestion instead of guessing.
- Never commit changes unless the user explicitly asks.`

// workflowSystemPrompt 是 workflow.enabled 时追加的系统提示:把五阶段流程
// 与工具用法固化进主 Agent 的指令,与门禁中间件形成软硬两层约束。
const workflowSystemPrompt = `

# 强制开发流程(游戏项目)
你处于强制五阶段流水线中,必须严格按顺序执行,不允许跳步:
0. init(读策划案):如配置了飞书(require_plan),先用 read_feishu_doc 读取策划案文档
1. flow_analysis(流程梳理):产出 docs/flow.md(玩法规则、房间状态机、结算逻辑、边界情况)
2. module_design(模块划分):产出 docs/modules.md(模块职责/接口/依赖)。产出后必须调用 request_review 提交人工审核,审核通过前禁止写任何代码
3. code_gen(代码生成):按审核通过的模块文档实现代码,go build 通过
4. self_test(自测):编写测试,go test 全绿
5. qa_cases(QA 测试案例):产出 docs/qa_cases.md 用例表,并用 write_feishu_doc 写回飞书

工具约定:
- 用 workflow_status 查看当前阶段与产出物状态
- 用 advance_stage 推进阶段(内部校验退出条件,失败会告诉你缺什么)
- 用 request_review 提交模块审核(仅在 module_design 阶段)
- 每个阶段只做该阶段允许的事,越界写操作会被门禁拦截并提示原因`

// Session 管理一条连接的全部运行态。
type Session struct {
	id       string
	provider *config.ProviderConfig
	workDir  string
	conn     *wsConn

	mu       sync.Mutex
	agent    *adk.ChatModelAgent
	buildErr error
	history  []*schema.Message

	teamMgr    *teams.TeamManager
	wtMgr      *worktree.Manager
	mcpMgr     *mcp.ClientManager
	fileHist   *filehistory.History
	extractor  *extractor.Extractor
	consol     *consolidation.Consolidator
	progressCh chan agents.SubAgentProgress
	askCh      chan tools.AskUserRequest

	// wfState/wfStore 是工作流强制流程的运行时状态(workflow.enabled 时非 nil)。
	wfState *workflow.WorkflowState
	wfStore *workflow.Store
	wfDef   workflow.WorkflowDef // 流程定义(config 下沉)

	// reviewCh/pendingReviews 是人工审核桥(RequestReviewTool ↔ review_request/
	// review_response 事件),与 askCh 分离:审核 UI 是带文档预览的专用面板。
	reviewCh       chan tools.ReviewRequest
	pendingReviews map[string]chan tools.ReviewResponse

	// feishu 是飞书 OpenAPI 客户端(feishu.enabled 时非 nil)。
	feishu *feishu.Client

	turnMu     sync.Mutex // 同时只跑一轮
	cancelMu   sync.Mutex // 保护 turnCancel：abortTurn 与 runTurn 不同 goroutine
	turnCtx    context.Context
	turnCancel context.CancelFunc

	pendingMu    sync.Mutex
	pendingAsks  map[string]chan tools.QuestionResponse
	pendingPerms map[string]chan bool
	seq          int
}

func NewSession(provider *config.ProviderConfig, workDir string, conn *wsConn) *Session {
	return &Session{
		id:           newID(),
		provider:     provider,
		workDir:      workDir,
		conn:         conn,
		teamMgr:      teams.NewTeamManager(workDir),
		wtMgr:        worktree.NewManager(),
		progressCh:   make(chan agents.SubAgentProgress, 64),
		askCh:        make(chan tools.AskUserRequest, 4),
		pendingAsks:  make(map[string]chan tools.QuestionResponse),
		pendingPerms: make(map[string]chan bool),

		reviewCh:       make(chan tools.ReviewRequest, 4),
		pendingReviews: make(map[string]chan tools.ReviewResponse),
	}
}

// Serve 跑上行分发循环直到连接断开；退出时清理后台队友。
func (s *Session) Serve() {
	go s.pumpAskUser()
	go s.pumpReview()
	go s.pumpProgress()

	s.conn.readLoop(func(env Envelope) {
		switch env.Type {
		case EvtUserMessage:
			var data UserMessageData
			if err := json.Unmarshal(env.Data, &data); err == nil && strings.TrimSpace(data.Content) != "" {
				go s.runTurn(data.Content)
			}
		case EvtAskUserResponse:
			var data AskUserResponseData
			if err := json.Unmarshal(env.Data, &data); err == nil {
				s.deliverAsk(data)
			}
		case EvtReviewResponse:
			var data ReviewResponseData
			if err := json.Unmarshal(env.Data, &data); err == nil {
				s.deliverReview(data)
			}
		case EvtPermissionResult:
			var data PermissionResponseData
			if err := json.Unmarshal(env.Data, &data); err == nil {
				s.deliverPermission(data)
			}
		case EvtAbort:
			s.abortTurn()
		}
	})

	s.teamMgr.CloseAll()
	if s.mcpMgr != nil {
		_ = s.mcpMgr.Close()
	}
	s.conn.Close()
}

// --- Agent 懒组装 ---

// ensureAgent 首次调用时组装完整 Agent；失败结果缓存，错误直达前端。
func (s *Session) ensureAgent(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agent != nil {
		return s.buildErr
	}
	s.buildErr = s.buildAgent(ctx)
	return s.buildErr
}

func (s *Session) buildAgent(ctx context.Context) error {
	chatModel, err := llm.NewChatModel(ctx, s.provider)
	if err != nil {
		return err
	}

	workDir := s.workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}

	sandbox := permissions.NewPathSandbox(workDir)
	ruleEngine := permissions.NewRuleEngine(workDir)
	mode := permissions.ModeDefault
	switch s.permModeFromConfig() {
	case string(permissions.ModeAcceptEdits):
		mode = permissions.ModeAcceptEdits
	case string(permissions.ModeBypass):
		mode = permissions.ModeBypass
	}

	s.fileHist = filehistory.New(workDir, s.id)

	deps := middleware.Dependencies{
		RecoveryState:  compact.NewRecoveryState(),
		Sandbox:        sandbox,
		RuleEngine:     ruleEngine,
		PermissionMode: mode,
		FileHistory:    s.fileHist,
	}
	appCfg, cfgErr := config.LoadConfig("")
	if cfgErr == nil {
		if len(appCfg.Hooks) > 0 {
			deps.HookConfigs = appCfg.Hooks
		}
		// 配置了 MCP Server 时创建客户端管理器，Agent 组装时自动连接并注册工具
		if len(appCfg.MCPServers) > 0 {
			s.mcpMgr = mcp.NewClientManager(appCfg.MCPServers)
		}
		// 工作流强制流程:加载项目级状态(断点续跑)并接入门禁中间件。
		if appCfg.Workflow.Enabled {
			wfDef := appCfg.Workflow.ToDef()
			if err := wfDef.Validate(); err != nil {
				return fmt.Errorf("workflow 配置非法: %w", err)
			}
			wfStore := workflow.NewStore(workDir)
			wfState := wfStore.Load()
			deps.Workflow = &middleware.WorkflowRuntime{Def: wfDef, State: wfState, Store: wfStore, WorkDir: workDir}
			s.wfState = wfState
			s.wfStore = wfStore
			s.wfDef = wfDef
		}
		// 飞书 client 模式:配置了 app_id/app_secret 时启用读写工具。
		if appCfg.Feishu.Enabled {
			s.feishu = feishu.NewClient(appCfg.Feishu)
		}
	}
	mwReg := middleware.NewRegistry(deps)

	// PermissionMW 由 Registry 建在链里，但 ConfirmFunc 要桥到 WebSocket：
	// 从链中定位实例回填（找不到时保持 Registry 默认行为：Ask 拒绝）。
	if pmw := findPermissionMW(mwReg); pmw != nil {
		pmw.ConfirmFunc = s.confirmViaWS
	}

	toolReg := tools.NewRegistry()

	// Lead 主任务板；队友用各自团队的任务板（BindTeammateTools 绑定）。
	mainList := todo.NewTaskList(todo.NewStore(workDir, "main"))

	// Skill 目录：三层合并加载(builtin→user→project)。清单注入 System
	// Prompt 供 LLM 按需 LoadSkill；目录引用同时交给 AgentOptions 以注册
	// LoadSkill/InstallSkill 工具。
	skillCatalog := skills.LoadCatalog(workDir)

	systemPrompt := baseSystemPrompt
	if s.wfState != nil {
		systemPrompt += workflowSystemPrompt
	}
	if skillSection := prompt.BuildSkillSection(skillCatalog, workDir); skillSection != "" {
		systemPrompt += skillSection
	}

	s.agent, err = agent.NewAgent(ctx, agent.AgentOptions{
		ChatModel:     chatModel,
		SystemPrompt:  systemPrompt,
		ToolRegistry:  toolReg,
		MWRegistry:    mwReg,
		Sandbox:       sandbox,
		RuleEngine:    ruleEngine,
		PermMode:      mode,
		MCPManager:    s.mcpMgr,
		SkillCatalog:  skillCatalog,
		WorkDir:       workDir,
		AskUserCh:     s.askCh,
		FileHistory:   s.fileHist,
		TeamMgr:       s.teamMgr,
		TaskList:      mainList,
		WorktreeMgr:   s.wtMgr,
		ProgressCh:    s.progressCh,
		WorkflowState: s.wfState,
		WorkflowStore: s.wfStore,
		WorkflowDef:   s.wfDef,
		ReviewCh:      s.reviewCh,
		FeishuClient:  s.feishu,
	})
	if err != nil {
		return err
	}

	// Lead 侧团队工具（SendMessage/TeamCreate/TeamDelete）注册在 Agent 工具
	// 之后：AgentTool.AllTools 已定格，Lead 靠 Agent/SendMessage 调度，
	// 这几个工具直接进主池即可。
	teams.RegisterTeamTools(toolReg.Register, s.teamMgr)

	// 记忆：提取器 + 整理器（turn 结束后异步触发）。
	home, _ := os.UserHomeDir()
	memDeps := extractor.Deps{
		ChatModel:     chatModel,
		SystemPrompt:  systemPrompt,
		MemoryDir:     filepath.Join(workDir, ".kcode", "memory") + string(os.PathSeparator),
		UserMemoryDir: filepath.Join(home, ".kcode", "memory") + string(os.PathSeparator),
		ProjectRoot:   workDir,
		ToolRegistry:  toolReg,
		AppendSystem: func(msg string) {
			s.conn.sendJSON(EvtUserTextEcho, StreamTextData{Text: msg})
		},
	}
	s.extractor = extractor.InitExtractMemories(memDeps)
	s.consol = consolidation.NewConsolidator(consolidation.Deps{
		ChatModel:     chatModel,
		SystemPrompt:  systemPrompt,
		MemoryDir:     memDeps.MemoryDir,
		UserMemoryDir: memDeps.UserMemoryDir,
		ProjectRoot:   workDir,
		ToolRegistry:  toolReg,
		AppendSystem:  memDeps.AppendSystem,
	})
	return nil
}

func (s *Session) permModeFromConfig() string {
	if cfg, err := config.LoadConfig(""); err == nil {
		return cfg.PermissionMode
	}
	return ""
}

// findPermissionMW 在中间件链里定位 PermissionMW 以便回填 ConfirmFunc。
func findPermissionMW(reg *middleware.Registry) *middleware.PermissionMW {
	for _, h := range reg.GetAll() {
		if pmw, ok := h.(*middleware.PermissionMW); ok {
			return pmw
		}
	}
	return nil
}

// --- 轮执行 ---

func (s *Session) runTurn(content string) {
	if !s.turnMu.TryLock() {
		s.conn.sendJSON(EvtError, ErrorData{Message: "agent is busy — wait for the current turn or send abort"})
		return
	}
	defer s.turnMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelMu.Lock()
	s.turnCtx = ctx
	s.turnCancel = cancel
	s.cancelMu.Unlock()
	defer func() {
		s.cancelMu.Lock()
		s.turnCtx = nil
		s.turnCancel = nil
		s.cancelMu.Unlock()
		cancel()
	}()

	if err := s.ensureAgent(ctx); err != nil {
		s.conn.sendJSON(EvtError, ErrorData{Message: err.Error()})
		return
	}

	// 文件快照：每轮开头打点，Rewind 可回滚到本轮之前的编辑状态。
	s.fileHist.MakeSnapshot(len(s.history), truncateRunes(content, 60))
	_ = s.fileHist.Save()

	inputs := make([]*schema.Message, 0, 4)
	// 团队通知进上下文（teammate idle / 消息回执）。
	for _, note := range teams.DrainLeadMailbox(s.teamMgr) {
		inputs = append(inputs, schema.UserMessage("<system-reminder>\n"+note+"\n</system-reminder>"))
	}
	inputs = append(inputs, schema.UserMessage(content))

	s.mu.Lock()
	full := append(append([]*schema.Message{}, s.history...), inputs...)
	s.mu.Unlock()

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: s.agent})
	iter := runner.Run(ctx, full)

	var (
		emitted []*schema.Message
		turnErr error
	)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil && turnErr == nil {
			turnErr = ev.Err
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		if msg := s.forwardEvent(ctx, ev.Output.MessageOutput); msg != nil {
			emitted = append(emitted, msg)
		}
	}

	s.mu.Lock()
	s.history = append(append([]*schema.Message{}, s.history...), inputs...)
	s.history = append(s.history, emitted...)
	s.mu.Unlock()

	if turnErr != nil {
		s.conn.sendJSON(EvtError, ErrorData{Message: turnErr.Error()})
	}
	if ctx.Err() != nil {
		s.conn.sendJSON(EvtError, ErrorData{Message: "turn aborted"})
	} else {
		s.conn.sendJSON(EvtTurnComplete, TurnCompleteData{})
	}

	// 回合结束：异步记忆提取 + 整理门控（不阻塞下一轮输入）。
	s.mu.Lock()
	histCopy := append([]*schema.Message{}, s.history...)
	s.mu.Unlock()
	go s.extractor.Execute(context.Background(), histCopy)
	go s.consol.MaybeRun(context.Background())
}

// forwardEvent 把一条模型/工具消息转成下行事件，并返回可回填 history 的
// 完整消息：流式事件边发 chunk 边收集，最后用 ConcatMessages 合并；
// tool_calls 从合并后的完整消息补发（流式 chunk 里的参数是半截的）。
func (s *Session) forwardEvent(ctx context.Context, mv *adk.TypedMessageVariant[*schema.Message]) *schema.Message {
	if mv == nil {
		return nil
	}
	switch {
	case mv.IsStreaming && mv.MessageStream != nil:
		var (
			chunks []*schema.Message
			text   strings.Builder
		)
		for {
			chunk, err := recvChunk(ctx, mv.MessageStream)
			if err != nil {
				break
			}
			if chunk == nil {
				continue
			}
			chunks = append(chunks, chunk)
			if chunk.Content != "" {
				text.WriteString(chunk.Content)
				s.conn.sendJSON(EvtStreamText, StreamTextData{Text: chunk.Content})
			}
		}
		if len(chunks) == 0 {
			return nil
		}
		merged, err := schema.ConcatMessages(chunks)
		if err != nil || merged == nil {
			// 合并失败退化为纯文本消息，至少保证多轮上下文不断。
			return schema.AssistantMessage(text.String(), nil)
		}
		switch merged.Role {
		case schema.Assistant:
			s.sendToolCalls(merged.ToolCalls)
		case schema.Tool:
			s.conn.sendJSON(EvtToolResult, ToolResultData{
				ID:     merged.ToolCallID,
				Name:   merged.ToolName,
				Output: merged.Content,
			})
		}
		return merged
	case mv.Message != nil:
		m := mv.Message
		switch m.Role {
		case schema.Assistant:
			if m.Content != "" {
				s.conn.sendJSON(EvtStreamText, StreamTextData{Text: m.Content})
			}
			s.sendToolCalls(m.ToolCalls)
		case schema.Tool:
			s.conn.sendJSON(EvtToolResult, ToolResultData{
				ID:     m.ToolCallID,
				Name:   m.ToolName,
				Output: m.Content,
			})
		}
		return m
	}
	return nil
}

func (s *Session) sendToolCalls(calls []schema.ToolCall) {
	for _, tc := range calls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		s.conn.sendJSON(EvtToolCall, ToolCallData{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		})
	}
}

func (s *Session) abortTurn() {
	s.cancelMu.Lock()
	cancel := s.turnCancel
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
		// 立即回执,避免前端只看到本地 notice、等 turn aborted 时误以为无响应。
		s.conn.sendJSON(EvtError, ErrorData{Message: "中止指令已送达,正在终止当前任务…"})
		return
	}
	s.conn.sendJSON(EvtError, ErrorData{Message: "当前没有正在执行的任务"})
}

// recvChunk 从模型流读一个 chunk;ctx 取消时立即返回。Eino 流式实现
// 若不响应 ctx 取消,裸调 Recv() 会永久阻塞,轮次收不了尾(表现为中止响应慢)。
func recvChunk(ctx context.Context, stream *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	type res struct {
		msg *schema.Message
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := stream.Recv()
		ch <- res{m, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.msg, r.err
	}
}

// --- ask_user / permission 桥 ---

// pumpAskUser 把 AskUserTool 发射的请求转成 ask_user 事件。
func (s *Session) pumpAskUser() {
	for req := range s.askCh {
		id := s.nextID("ask")
		ch := make(chan tools.QuestionResponse, 1)
		s.pendingMu.Lock()
		s.pendingAsks[id] = ch
		s.pendingMu.Unlock()

		s.conn.sendJSON(EvtAskUser, AskUserData{ID: id, Questions: req.Questions})

		select {
		case resp := <-ch:
			req.ResponseCh <- resp
		case <-s.conn.closed:
			// 连接断了：回空应答解除工具侧阻塞，Agent 轮会因 ctx 取消收尾。
			req.ResponseCh <- tools.QuestionResponse{}
		}
		s.pendingMu.Lock()
		delete(s.pendingAsks, id)
		s.pendingMu.Unlock()
	}
}

func (s *Session) deliverAsk(data AskUserResponseData) {
	s.pendingMu.Lock()
	ch, ok := s.pendingAsks[data.ID]
	s.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- tools.QuestionResponse{Answers: data.Answers}:
	default:
	}
}

// pumpReview 把 RequestReviewTool 发射的审核请求转成 review_request 事件,
// 等待 review_response 后把决策回灌工具侧(与 pumpAskUser 对称)。
func (s *Session) pumpReview() {
	for req := range s.reviewCh {
		id := s.nextID("review")
		ch := make(chan tools.ReviewResponse, 1)
		s.pendingMu.Lock()
		s.pendingReviews[id] = ch
		s.pendingMu.Unlock()

		s.conn.sendJSON(EvtReviewRequest, ReviewRequestData{
			ID:              id,
			ArtifactPath:    req.ArtifactPath,
			ArtifactContent: req.ArtifactContent,
			Stages:          req.Stages,
		})

		select {
		case resp := <-ch:
			req.ResponseCh <- resp
		case <-s.conn.closed:
			// 连接断了:回拒绝解除工具侧阻塞,Agent 轮会因 ctx 取消收尾。
			req.ResponseCh <- tools.ReviewResponse{}
		}
		s.pendingMu.Lock()
		delete(s.pendingReviews, id)
		s.pendingMu.Unlock()
	}
}

// deliverReview 分发用户审核决策给等待中的审核桥。
func (s *Session) deliverReview(data ReviewResponseData) {
	s.pendingMu.Lock()
	ch, ok := s.pendingReviews[data.ID]
	s.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- tools.ReviewResponse{
		Approved: data.Decision == "approve",
		Feedback: data.Feedback,
	}:
	default:
	}
}

// confirmViaWS 实现 PermissionMW.ConfirmFunc：发 permission_request
// 并阻塞等待应答；轮取消 / 连接断开按拒绝处理。
func (s *Session) confirmViaWS(toolName, description string) bool {
	id := s.nextID("perm")
	ch := make(chan bool, 1)
	s.pendingMu.Lock()
	s.pendingPerms[id] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pendingPerms, id)
		s.pendingMu.Unlock()
	}()

	s.conn.sendJSON(EvtPermissionRequest, PermissionRequestData{
		ID:          id,
		Tool:        toolName,
		Description: description,
	})

	select {
	case allow := <-ch:
		return allow
	case <-s.done():
		return false
	}
}

func (s *Session) deliverPermission(data PermissionResponseData) {
	s.pendingMu.Lock()
	ch, ok := s.pendingPerms[data.ID]
	s.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- data.Response == "allow":
	default:
	}
}

func (s *Session) done() <-chan struct{} {
	return s.conn.closed
}

// pumpProgress 转发子 Agent 进度事件。
func (s *Session) pumpProgress() {
	for p := range s.progressCh {
		elapsed := p.Elapsed
		if p.Done {
			elapsed = p.TotalTime
		}
		s.conn.sendJSON(EvtSubAgentProgress, SubAgentProgressData{
			Name:    p.AgentDesc,
			AgentID: p.AgentType,
			Tool:    p.ToolName,
			Elapsed: elapsed,
			IsError: p.IsError,
			Done:    p.Done,
		})
	}
}

// --- 小工具 ---

func (s *Session) nextID(prefix string) string {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.seq++
	return fmt.Sprintf("%s-%d", prefix, s.seq)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
