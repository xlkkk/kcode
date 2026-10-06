package middleware

import (
	"kcode/internal/compact"
	"kcode/internal/filehistory"
	"kcode/internal/hooks"
	"kcode/internal/permissions"
	"kcode/internal/workflow"

	"github.com/cloudwego/eino/adk"
)

// Registry 中间件注册表
//
// 管理所有已注册的 Middleware，按注册顺序构建 Middleware 链。
// 注册顺序即为执行顺序（从外到内）。
type Registry struct {
	handlers []adk.ChatModelAgentMiddleware
}

// NewRegistry 创建中间件注册表并注册所有默认中间件
func NewRegistry(deps Dependencies) *Registry {
	r := &Registry{handlers: make([]adk.ChatModelAgentMiddleware, 0)}
	// 按顺序注册默认中间件
	r.Register(NewRecoveryMW(deps.RecoveryState))
	if len(deps.HookConfigs) > 0 {
		r.Register(NewHooksMW(deps.HookConfigs))
	}
	// r.Register(NewReminderMW(deps.Reminders))
	// r.Register(NewCompactionMW(deps.CompactConfig))
	if deps.FileHistory != nil {
		r.Register(NewFileTrackMW(deps.FileHistory))
	}
	// WorkflowGateMW 门禁先于权限检查:阶段矩阵拦截比权限确认更靠外。
	if deps.Workflow != nil {
		r.Register(NewWorkflowGateMW(deps.Workflow.Def, deps.Workflow.State, deps.Workflow.Store, deps.Workflow.WorkDir))
	}
	if deps.Sandbox != nil && deps.RuleEngine != nil {
		r.Register(NewPermissionMW(deps.Sandbox, deps.RuleEngine, deps.PermissionMode))
	}
	// r.Register(NewResultBudgetMW(deps.BudgetConfig))
	// r.Register(NewSessionMW(deps.SessionConfig))
	return r
}

// WorkflowRuntime 工作流门禁的运行时依赖。非 nil 时注册 WorkflowGateMW。
type WorkflowRuntime struct {
	Def     workflow.WorkflowDef // 流程定义(config.yaml workflow 段下沉)
	State   *workflow.WorkflowState
	Store   *workflow.Store
	WorkDir string
}

// Dependencies 中间件依赖的基础设施组件
type Dependencies struct {
	RecoveryState  *compact.RecoveryState
	Sandbox        *permissions.PathSandbox
	RuleEngine     *permissions.RuleEngine
	PermissionMode permissions.PermissionMode
	HookConfigs    []hooks.Hook         // 用户配置的 hook 列表
	FileHistory    *filehistory.History // 文件快照/回滚;非 nil 时启用 FileTrackMW
	Workflow       *WorkflowRuntime     // 工作流门禁;非 nil 时启用 WorkflowGateMW
}

// Register 注册一个中间件
func (r *Registry) Register(h adk.ChatModelAgentMiddleware) {
	r.handlers = append(r.handlers, h)
}

// GetAll 获取所有已注册的中间件
func (r *Registry) GetAll() []adk.ChatModelAgentMiddleware {
	return r.handlers
}
