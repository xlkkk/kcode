// manager.go — 主 Agent 的 worktree 会话切换（EnterWorktree / ExitWorktree 的
// 支撑状态）。
//
// 备注 internal/worktree/session.go 的简化版。差异：不做包级全局
// 单例，Manager 实例由装配层持有、注入两个工具，天然支持多会话；进程级
// chdir 语义与 Code 一致（kcode 进程同时只跑一个主 Agent 循环）。

package worktree

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// Session 记录一次 EnterWorktree 的完整现场，Exit 时还原。
type Session struct {
	OriginalCwd        string
	RepoRoot           string
	WorktreePath       string
	WorktreeBranch     string
	OriginalHeadCommit string
}

// Manager 持有当前 worktree 会话（同一时刻至多一个）。
type Manager struct {
	mu  sync.Mutex
	cur *Session
}

func NewManager() *Manager { return &Manager{} }

// Current 返回活动会话；不在 worktree 里返回 nil。
func (m *Manager) Current() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	s := *m.cur
	return &s
}

// Enter 创建（或恢复）worktree 并把进程工作目录切进去。
func (m *Manager) Enter(ctx context.Context, slug, repoRoot string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		return nil, fmt.Errorf("already in a worktree session (%s)", m.cur.WorktreePath)
	}

	if err := ValidateWorktreeSlug(slug); err != nil {
		return nil, err
	}
	if repoRoot == "" {
		repoRoot = FindCanonicalGitRoot(mustGetwd())
	}
	if repoRoot == "" {
		return nil, fmt.Errorf("not in a git repository")
	}

	originalCwd := mustGetwd()
	result, err := getOrCreateWorktree(ctx, repoRoot, slug)
	if err != nil {
		return nil, err
	}
	if !result.Existed {
		performPostCreationSetup(ctx, repoRoot, result.WorktreePath)
	}

	if err := os.Chdir(result.WorktreePath); err != nil {
		return nil, fmt.Errorf("chdir worktree: %w", err)
	}

	m.cur = &Session{
		OriginalCwd:        originalCwd,
		RepoRoot:           repoRoot,
		WorktreePath:       result.WorktreePath,
		WorktreeBranch:     result.WorktreeBranch,
		OriginalHeadCommit: result.HeadCommit,
	}
	return m.cur, nil
}

// Keep 退出会话回到原目录，worktree 与分支留在磁盘上。
func (m *Manager) Keep() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return fmt.Errorf("no active worktree session")
	}
	if err := os.Chdir(m.cur.OriginalCwd); err != nil {
		return fmt.Errorf("chdir back: %w", err)
	}
	m.cur = nil
	return nil
}

// Cleanup 退出会话回到原目录，并删除 worktree 与分支。
func (m *Manager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return fmt.Errorf("no active worktree session")
	}
	if err := os.Chdir(m.cur.OriginalCwd); err != nil {
		return fmt.Errorf("chdir back: %w", err)
	}
	if !RemoveAgentWorktree(ctx, m.cur.WorktreePath, m.cur.WorktreeBranch, m.cur.RepoRoot) {
		return fmt.Errorf("failed to remove worktree at %s", m.cur.WorktreePath)
	}
	m.cur = nil
	return nil
}

func mustGetwd() string {
	cwd, _ := os.Getwd()
	return cwd
}
