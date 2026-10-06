package skills

import "context"

// SkillHost Agent 提供给 Executor 的接口。
// 避免 skills 包直接依赖 agent 包（防止循环引用）。
type SkillHost interface {
	// ActivateSkill 记录 skill 激活（用于 /skills 列表和压缩恢复）。
	// body 不会每轮重新注入——它作为普通消息留在对话中。
	ActivateSkill(name, body string)
}

// RunInline 执行 inline 模式 skill：渲染正文并记录激活。
// 调用方将返回的 body 作为用户消息提交到主对话。
func RunInline(_ context.Context, skill *Skill, args string, host SkillHost) (string, error) {
	body := skill.Render(args)
	host.ActivateSkill(skill.Meta.Name, body)
	return body, nil
}
