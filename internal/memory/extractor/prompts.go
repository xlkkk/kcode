// Package extractor 实现后台记忆提取子 Agent。
//
// 提取 Agent 以主对话的"完美 fork"运行 —— 同样的 System Prompt、同样的
// 消息前缀，末尾追加一条提取指令。主 Agent 的 System Prompt 里始终带有
// 完整的记忆保存指令；当主 Agent 自己写了记忆文件时，extractor.Execute 会
// 跳过该轮（hasMemoryWritesSince）。
//
// 备注 internal/memory/extractor/prompts.go

package extractor

import (
	"fmt"
	"strings"

	"kcode/internal/memory"
)

// kcode 工具注册表中的工具名（小写下划线风格），这些字符串烧进 prompt，
// 保证模型发出的调用能被 Registry 识别。
const (
	bashToolName      = "bash"
	fileEditToolName  = "edit_file"
	fileReadToolName  = "read_file"
	fileWriteToolName = "write_file"
	globToolName      = "glob"
	grepToolName      = "grep"
)

// opener 是两种提取 prompt 变体共用的开场段落。
func opener(newMessageCount int, existingMemories string) string {
	manifest := ""
	if existingMemories != "" {
		manifest = fmt.Sprintf(
			"\n\n## Existing memory files\n\n%s\n\nCheck this list before writing — update an existing file rather than creating a duplicate.",
			existingMemories)
	}
	return strings.Join([]string{
		fmt.Sprintf("You are now acting as the memory extraction subagent. Analyze the most recent ~%d messages above and use them to update your persistent memory systems.", newMessageCount),
		"",
		fmt.Sprintf("Available tools: %s, %s, %s, read-only %s (ls/find/cat/stat/wc/head/tail and similar), and %s/%s for paths inside the memory directory only. %s rm is not permitted. All other tools — MCP, Agent, write-capable %s, etc — will be denied.",
			fileReadToolName, grepToolName, globToolName, bashToolName,
			fileEditToolName, fileWriteToolName,
			bashToolName, bashToolName),
		"",
		fmt.Sprintf("You have a limited turn budget. %s requires a prior %s of the same file, so the efficient strategy is: turn 1 — issue all %s calls in parallel for every file you might update; turn 2 — issue all %s/%s calls in parallel. Do not interleave reads and writes across multiple turns.",
			fileEditToolName, fileReadToolName, fileReadToolName, fileWriteToolName, fileEditToolName),
		"",
		fmt.Sprintf("You MUST only use content from the last ~%d messages to update your persistent memories. Do not waste any turns attempting to investigate or verify that content further — no grepping source files, no reading code to confirm a pattern exists, no git commands.%s",
			newMessageCount, manifest),
	}, "\n")
}

// BuildExtractAutoOnlyPrompt 构建自动记忆提取 prompt（无团队记忆模式）。
// 四类型分类 + 双路径存储：user/feedback 记忆写入 userMemDir，
// project/reference 记忆写入 projectMemDir。
//
// userMemDir 可以为 ""（$HOME 无法解析时）—— 此时 prompt 退化为仅项目路由，
// user/feedback 类型隐式不可用。
func BuildExtractAutoOnlyPrompt(newMessageCount int, existingMemories string, skipIndex bool, userMemDir, projectMemDir string) string {
	var routing strings.Builder
	routing.WriteString("## Memory storage paths\n\n")
	if userMemDir != "" {
		fmt.Fprintf(&routing, "- `user` and `feedback` type → write to `%s` (user-level; follows the human across projects)\n", userMemDir)
	}
	if projectMemDir != "" {
		fmt.Fprintf(&routing, "- `project` and `reference` type → write to `%s` (project-level; lives with this repo)\n", projectMemDir)
	}
	routing.WriteString("\nPick the type first, then write the memory file (and its MEMORY.md pointer) into the matching directory. Never write a `user`/`feedback` memory into the project-level dir or vice versa.")

	var howToSave []string
	if skipIndex {
		howToSave = []string{
			"## How to save memories",
			"",
			"Write each memory to its own file (e.g., `user_role.md`, `feedback_testing.md`) using this frontmatter format:",
			"",
			memory.MemoryFrontmatterExample,
			"",
			"- Organize memory semantically by topic, not chronologically",
			"- Update or remove memories that turn out to be wrong or outdated",
			"- Do not write duplicate memories. First check if there is an existing memory you can update before writing a new one.",
		}
	} else {
		howToSave = []string{
			"## How to save memories",
			"",
			"Saving a memory is a two-step process:",
			"",
			"**Step 1** — write the memory to its own file (e.g., `user_role.md`, `feedback_testing.md`) in the directory that matches its `type` (see Memory storage paths above), using this frontmatter format:",
			"",
			memory.MemoryFrontmatterExample,
			"",
			"**Step 2** — add a pointer to that file in the `MEMORY.md` index in the SAME directory. `MEMORY.md` is an index, not a memory — each entry should be one line, under ~150 characters: `- [Title](file.md) — one-line hook`. It has no frontmatter. Never write memory content directly into `MEMORY.md`.",
			"",
			"- Both `MEMORY.md` files (user-level and project-level) are loaded into context — lines after 200 each will be truncated, so keep each index concise",
			"- Organize memory semantically by topic, not chronologically",
			"- Update or remove memories that turn out to be wrong or outdated",
			"- Do not write duplicate memories. First check if there is an existing memory you can update before writing a new one.",
		}
	}

	parts := []string{
		opener(newMessageCount, existingMemories),
		"",
		"If the user explicitly asks you to remember something, save it immediately as whichever type fits best. If they ask you to forget something, find and remove the relevant entry.",
		"",
		routing.String(),
		"",
		memory.TypesSectionDualPath,
		memory.WhatNotToSaveSection,
		"",
	}
	parts = append(parts, howToSave...)
	return strings.Join(parts, "\n")
}
