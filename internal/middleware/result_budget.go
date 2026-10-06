package middleware

// ResultBudgetMW — 工具结果预算 Middleware
//
// 钩子：AfterModelRewriteState
// 职责：工具结果进入历史后处理
//   - 单条 > 50000 字符 → 完整内容写盘，历史里只留预览 + 文件路径
//   - 单消息聚合超限 → 同样溢写
//   - 溢写文件的回读结果豁免（防止模型反复读自己的溢写文件）
//
// 备注 agent.go 循环 ⑦ 的 toolresult.PersistLargeResult / ApplyBudget
type ResultBudgetMW struct{}
