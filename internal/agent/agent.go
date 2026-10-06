package agent

import (
	"context"
	"sync"

	"kcode/internal/agents"
	"kcode/internal/feishu"
	"kcode/internal/filehistory"
	"kcode/internal/mcp"
	"kcode/internal/middleware"
	"kcode/internal/permissions"
	"kcode/internal/skills"
	"kcode/internal/teams"
	"kcode/internal/todo"
	"kcode/internal/tools"
	"kcode/internal/workflow"
	"kcode/internal/worktree"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// AgentOptions 组装 Agent 所需的全部配置
type AgentOptions struct {
	ChatModel    model.BaseModel[*schema.Message] // Eino ChatModel（由 eino-ext 提供）
	SystemPrompt string
	ToolRegistry *tools.Registry
	MWRegistry   *middleware.Registry
	MCPManager   *mcp.ClientManager         // MCP 客户端管理器（可选）
	Sandbox      *permissions.PathSandbox   // 路径沙箱（可选）
	RuleEngine   *permissions.RuleEngine    // 规则引擎（可选）
	PermMode     permissions.PermissionMode // 权限模式（可选）
	SkillCatalog *skills.Catalog            // Skill 目录（可选）
	WorkDir      string                     // 工作目录（Skill 热重载用）

	// 移植功能的新依赖（全部可选：为 nil 时对应能力不注册）
	AskUserCh     chan<- tools.AskUserRequest                            // HITL 提问出口（Web 层监听）
	FileHistory   *filehistory.History                                   // 文件快照/回滚（子 Agent 的 ExtraHandlers）
	TeamMgr       *teams.TeamManager                                     // 团队管理器（SpawnTeammate 钩子）
	TaskList      *todo.TaskList                                         // Lead 主任务板（nil 时不注册 Task* 工具）
	WorktreeMgr   *worktree.Manager                                      // Enter/ExitWorktree 会话管理器
	AgentLoader   *agents.AgentLoader                                    // Agent 定义加载器（nil 时自动创建）
	TaskMgr       *agents.TaskManager                                    // 异步子 Agent 任务表（nil 时自动创建）
	ProgressCh    chan<- agents.SubAgentProgress                         // 子 Agent 进度事件出口（可为 nil）
	ModelResolver func(string) (model.BaseModel[*schema.Message], error) // 按模型名解析（可为 nil）

	// 工作流强制流程(可选:State/Store 任一为 nil 时不注册工作流工具)
	WorkflowState *workflow.WorkflowState
	WorkflowStore *workflow.Store
	WorkflowDef   workflow.WorkflowDef // 流程定义(默认 DefaultDef)

	// 审核交互通道(Web 层监听,转 review_request 事件);飞书客户端
	// (feishu.enabled 时非 nil,注册 read/write_feishu_doc)。
	ReviewCh     chan<- tools.ReviewRequest
	FeishuClient *feishu.Client
}

// NewAgent 组装 ChatModelAgent + Middleware 链
//
// Middleware 顺序（从外到内）：
//  1. HooksMW        — 生命周期钩子（session start/end, turn start/end）
//  2. ReminderMW     — system-reminder 注入（Plan/Coordinator/Skill/Notification）
//  3. CompactionMW   — 上下文压缩（token 超阈值时自动摘要）
//  4. PermissionMW   — 权限检查（5 层防御）
//  5. ResultBudgetMW — 工具结果预算（大结果溢写）
//  6. SessionMW      — 会话持久化（每轮消息落盘）
//  7. RecoveryMW     — 恢复状态（文件快照、Skill 快照）
func NewAgent(ctx context.Context, opts AgentOptions) (*adk.ChatModelAgent, error) {
	// 1. 连接 MCP Server 并注册 MCP 工具
	if opts.MCPManager != nil {
		if err := opts.MCPManager.ConnectAll(ctx); err == nil {
			mcpTools := mcp.ConvertMCPTools(opts.MCPManager)
			for _, t := range mcpTools {
				opts.ToolRegistry.Register(t)
			}
		}
	}

	// 2. 注册 Skill 工具（LoadSkill + InstallSkill）
	if opts.SkillCatalog != nil {
		host := &skillHost{active: make(map[string]string)}
		opts.ToolRegistry.Register(skills.NewLoadSkillTool(opts.SkillCatalog, host))
		opts.ToolRegistry.Register(skills.NewInstallSkillTool(opts.SkillCatalog, nil))
	}

	// 2.5 注册移植功能的新工具：AskUser、Enter/ExitWorktree。
	// Agent 工具最后注册（需要完整工具池），见步骤 2.6。
	if opts.AskUserCh != nil {
		opts.ToolRegistry.Register(tools.NewAskUserTool(opts.AskUserCh))
	}
	if opts.WorktreeMgr != nil {
		opts.ToolRegistry.Register(tools.NewEnterWorktreeTool(opts.WorktreeMgr, ""))
		opts.ToolRegistry.Register(tools.NewExitWorktreeTool(opts.WorktreeMgr))
	}
	if opts.TaskList != nil {
		opts.ToolRegistry.Register(todo.NewTaskCreateTool(opts.TaskList))
		opts.ToolRegistry.Register(todo.NewTaskGetTool(opts.TaskList))
		opts.ToolRegistry.Register(todo.NewTaskListTool(opts.TaskList))
		opts.ToolRegistry.Register(todo.NewTaskUpdateTool(opts.TaskList))
	}

	// 2.6 组装 Agent 工具（子 Agent / teammate 派发入口）+ ParentStateMW（fork 数据源）。
	if err := registerAgentTool(ctx, opts); err != nil {
		return nil, err
	}

	// 2.7 注册工作流工具。必须在 Agent 工具之后:AgentTool 内部已定格工具池
	// 快照,此后注册的工具子 Agent 不可见,保证 request_review/advance_stage
	// 是 Lead 专属,子 Agent 无法跳阶段。
	if opts.WorkflowState != nil && opts.WorkflowStore != nil {
		opts.ToolRegistry.Register(tools.NewRequestReviewTool(opts.ReviewCh, opts.WorkflowState, opts.WorkflowStore, opts.WorkDir))
		opts.ToolRegistry.Register(tools.NewAdvanceStageTool(opts.WorkflowDef, opts.WorkflowState, opts.WorkflowStore, opts.WorkDir))
		opts.ToolRegistry.Register(tools.NewWorkflowStatusTool(opts.WorkflowState))
		opts.ToolRegistry.Register(tools.NewSimulateMatchTool(opts.WorkflowState, opts.WorkflowStore, opts.WorkDir))
	}

	// 2.8 注册飞书读写工具(策划案读取 / QA 案例写回)。同样在 Agent 工具
	// 定格之后:飞书读写是 Lead 专属的流程动作,子 Agent 不参与。
	if opts.FeishuClient != nil {
		opts.ToolRegistry.Register(tools.NewReadFeishuDocTool(opts.FeishuClient, opts.WorkflowState, opts.WorkflowStore))
		opts.ToolRegistry.Register(tools.NewWriteFeishuDocTool(opts.FeishuClient, opts.WorkDir))
	}

	// 3. 构建工具列表
	allTools := opts.ToolRegistry.GetAll()

	// 4. 构建 Middleware 链（从 Registry 获取）
	handlers := opts.MWRegistry.GetAll()

	// 5. 创建 ChatModelAgent
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Model:         opts.ChatModel,    // chatmodel接口
		Instruction:   opts.SystemPrompt, // 系统提示词
		MaxIterations: 200,               // 最大迭代次数
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: allTools,
			},
		},
		Handlers: handlers,
	})
	if err != nil {
		return nil, err
	}

	return agent, nil
}

// skillHost 实现 skills.SkillHost 接口
//
// 记录已激活的 Skill（用于 /skills 列表和压缩恢复）。
type skillHost struct {
	mu     sync.Mutex
	active map[string]string // name → body
}

func (h *skillHost) ActivateSkill(name, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[name] = body
}

func (h *skillHost) GetActiveSkills() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]string, len(h.active))
	for k, v := range h.active {
		out[k] = v
	}
	return out
}
