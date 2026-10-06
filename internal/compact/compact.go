package compact

// 压缩算法
// - EstimateTokens: 3.5 chars/token 近似估算
// - ComputeThreshold: 计算压缩触发线
// - Summarize: 调 LLM 生成摘要
// - ApplySummary: 替换历史前缀 + 保留近期尾部
// 备注 internal/compact/compact.go
