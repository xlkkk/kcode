package middleware

// CompactionMW — 上下文压缩 Middleware
//
// 钩子：BeforeModelRewriteState
// 职责：每次模型调用前检查 token 用量，超阈值时调 LLM 生成摘要
//   - 软触发线：effectiveWindow - 13000 → 自动压缩
//   - 硬触发线：effectiveWindow - 3000  → 强制压缩
//   - 压缩后保留近期消息尾部（≥10000 token 或 ≥5 条）
//   - 可复用 Eino 内置 Summarization 中间件
//
// 备注 agent.go 循环 ② compact.ManageContext()
type CompactionMW struct{}
