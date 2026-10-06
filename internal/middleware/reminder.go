package middleware

// ReminderMW — system-reminder 注入 Middleware
//
// 钩子：BeforeModelRewriteState
// 职责：每轮模型调用前注入上下文提醒
//   - Plan Mode reminder（plan 路径、plan 是否存在）
//   - Coordinator Mode reminder（团队调度指引）
//   - Notification（来自 hooks 的通知）
//   - Skill delta（中途新增的 Skill）
//   - Deferred tool names（延迟加载的 MCP 工具名）
//   - Memory recall（非阻塞记忆检索结果）
//
// 备注 agent.go 循环 ① 的各段注入逻辑
type ReminderMW struct{}
