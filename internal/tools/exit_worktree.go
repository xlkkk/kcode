// exit_worktree.go — ExitWorktree 工具：退出 EnterWorktree 会话并还原工作目录。
//
// 备注 internal/tools/exit_worktree.go（Eino InvokableTool 版）

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"kcode/internal/worktree"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ExitWorktreeTool struct {
	Mgr *worktree.Manager
}

var _ tool.InvokableTool = &ExitWorktreeTool{}

func NewExitWorktreeTool(mgr *worktree.Manager) *ExitWorktreeTool {
	return &ExitWorktreeTool{Mgr: mgr}
}

func (t *ExitWorktreeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "ExitWorktree",
		Desc: "Exits a worktree session created by EnterWorktree and restores the original working directory",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"action": {
				Type:     schema.String,
				Desc:     `"keep" leaves the worktree and branch on disk; "remove" deletes both.`,
				Enum:     []string{"keep", "remove"},
				Required: true,
			},
			"discard_changes": {
				Type: schema.Boolean,
				Desc: `Required true when action is "remove" and the worktree has uncommitted files or unmerged commits. The tool will refuse and list them otherwise.`,
			},
		}),
	}, nil
}

func (t *ExitWorktreeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Action         string `json:"action"`
		DiscardChanges bool   `json:"discard_changes"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// 作用域守卫：只操作 EnterWorktree 在本会话创建的 worktree。
	session := t.Mgr.Current()
	if session == nil {
		return "No-op: there is no active EnterWorktree session to exit. This tool only operates on worktrees created by EnterWorktree in the current session — it will not touch worktrees created manually or in a previous session. No filesystem changes were made.", nil
	}

	// remove 且未显式丢弃时，先核对变更（fail-closed）。
	if args.Action == "remove" && !args.DiscardChanges {
		summary := worktree.CountWorktreeChanges(ctx, session.WorktreePath, session.OriginalHeadCommit)
		if summary == nil {
			return fmt.Sprintf(
				"Could not verify worktree state at %s. Refusing to remove without explicit confirmation. Re-invoke with discard_changes: true to proceed — or use action: \"keep\" to preserve the worktree.",
				session.WorktreePath,
			), nil
		}
		if summary.ChangedFiles > 0 || summary.Commits > 0 {
			var parts []string
			if summary.ChangedFiles > 0 {
				word := "files"
				if summary.ChangedFiles == 1 {
					word = "file"
				}
				parts = append(parts, fmt.Sprintf("%d uncommitted %s", summary.ChangedFiles, word))
			}
			if summary.Commits > 0 {
				word := "commits"
				if summary.Commits == 1 {
					word = "commit"
				}
				branchName := session.WorktreeBranch
				if branchName == "" {
					branchName = "the worktree branch"
				}
				parts = append(parts, fmt.Sprintf("%d %s on %s", summary.Commits, word, branchName))
			}
			return fmt.Sprintf(
				"Worktree has %s. Removing will discard this work permanently. Confirm with the user, then re-invoke with discard_changes: true — or use action: \"keep\" to preserve the worktree.",
				strings.Join(parts, " and "),
			), nil
		}
	}

	worktreePath := session.WorktreePath
	worktreeBranch := session.WorktreeBranch
	originalCwd := session.OriginalCwd

	if args.Action == "keep" {
		if err := t.Mgr.Keep(); err != nil {
			return fmt.Sprintf("Error keeping worktree: %s", err), nil
		}
		branchInfo := ""
		if worktreeBranch != "" {
			branchInfo = " on branch " + worktreeBranch
		}
		return fmt.Sprintf(
			"Exited worktree. Your work is preserved at %s%s. Session is now back in %s.",
			worktreePath, branchInfo, originalCwd,
		), nil
	}

	// action == "remove"
	if err := t.Mgr.Cleanup(ctx); err != nil {
		return fmt.Sprintf("Error removing worktree: %s", err), nil
	}
	return fmt.Sprintf(
		"Exited and removed worktree at %s. Session is now back in %s.",
		worktreePath, originalCwd,
	), nil
}
