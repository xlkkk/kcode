package middleware

// SessionMW — 会话持久化 Middleware
//
// 钩子：
//   - BeforeAgent            → 恢复上次会话（如有）
//   - AfterModelRewriteState → 每轮结束持久化消息到 JSONL
//
// 备注 agent.go persistLastMessage()
type SessionMW struct{}
