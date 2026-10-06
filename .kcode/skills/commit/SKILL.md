---
name: commit
description: 生成规范的 git commit message
---

# Commit Skill

1. 查看 `git diff --cached` 获取暂存区变更
2. 分析变更内容，按 Conventional Commits 格式生成 message
3. 格式: `<type>(<scope>): <subject>`
4. type: feat/fix/docs/style/refactor/test/chore
