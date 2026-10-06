package toolresult

// 工具结果预算
// - PersistLargeResult: >50000 字符的结果写盘换预览
// - ApplyBudget: 单消息聚合预算检查
// - IsSpillReadback: 检测溢写文件的回读（豁免溢写）
// 备注 internal/toolresult/budget.go
