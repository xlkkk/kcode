// builtins.go — 内置 Agent 定义。
//
// 备注 internal/agents/subagent.go 的 BuiltinSpecs 部分。
// 差异：去掉 plan agent（kcode 不实现 plan 能力）；工具名用小写蛇形
// （read_file / grep / glob / bash ...）与 tools.Registry 保持一致。

package agents

// exploreAgentSystemPrompt 是只读搜索 Agent 的系统提示词。
const exploreAgentSystemPrompt = `You are a fast read-only search agent for locating code.

=== CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS ===
You are STRICTLY PROHIBITED from creating, modifying, or deleting any files, and from running
any state-changing commands.

## Your Process

1. Use read_file to inspect files and understand code.
2. Use grep to find patterns, function definitions, and references.
3. Use glob to discover file structure.
4. Use bash ONLY for read-only operations (ls, find, git log/diff/show, cat, head, tail).
   NEVER use bash for: mkdir, touch, rm, cp, mv, git add/commit, npm install.

## Output

Report concrete file paths with line numbers where possible, and short excerpts that answer the
question. If you cannot find something, say so explicitly — do not speculate.`

// BuiltinSpecs 是内置 Agent 规格表，加载器把它们直接转成 AgentDefinition，
// 不走文件解析。
var BuiltinSpecs = map[string]SubAgentSpec{
	"general-purpose": {
		Name:        "general-purpose",
		Description: "General-purpose agent for research and multi-step tasks. Full tool access.",
		MaxTurns:    200,
	},
	"explore": {
		Name:                 "explore",
		Description:          "Fast read-only search agent for locating code. Use it to find files by pattern, grep for symbols, or answer \"where is X defined\" questions.",
		DisallowedTools:      []string{"write_file", "edit_file"},
		SystemPromptOverride: exploreAgentSystemPrompt,
		OmitKcodeMd:          true,
	},
}
