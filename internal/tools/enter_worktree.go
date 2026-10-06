// enter_worktree.go — EnterWorktree 工具：创建隔离 git worktree 并切入。
//
// 备注 internal/tools/enter_worktree.go（Eino InvokableTool 版）

package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"kcode/internal/worktree"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type EnterWorktreeTool struct {
	Mgr      *worktree.Manager
	RepoRoot string // 为空时在调用点探测 git 根
}

var _ tool.InvokableTool = &EnterWorktreeTool{}

func NewEnterWorktreeTool(mgr *worktree.Manager, repoRoot string) *EnterWorktreeTool {
	return &EnterWorktreeTool{Mgr: mgr, RepoRoot: repoRoot}
}

func (t *EnterWorktreeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "EnterWorktree",
		Desc: "Creates an isolated worktree (via git) and switches the session into it",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Type: schema.String,
				Desc: `Optional name for the worktree. Each "/"-separated segment may contain only letters, digits, dots, underscores, and dashes; max 64 chars total. A random name is generated if not provided.`,
			},
		}),
	}, nil
}

func (t *EnterWorktreeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	if t.Mgr.Current() != nil {
		return "Error: already in a worktree session", nil
	}

	slug := args.Name
	if slug == "" {
		slug = generateWorktreeSlug()
	}

	session, err := t.Mgr.Enter(ctx, slug, t.RepoRoot)
	if err != nil {
		return fmt.Sprintf("Error creating worktree: %s", err), nil
	}

	branchInfo := ""
	if session.WorktreeBranch != "" {
		branchInfo = " on branch " + session.WorktreeBranch
	}
	return fmt.Sprintf(
		"Created worktree at %s%s. The session is now working in the worktree. Use ExitWorktree to leave mid-session, or exit the session to be prompted.",
		session.WorktreePath, branchInfo,
	), nil
}

func generateWorktreeSlug() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "wt-" + hex.EncodeToString(b)
}
