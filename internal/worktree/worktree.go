package worktree

// Package worktree — git worktree 隔离：为子 Agent / 队友创建独立工作副本。
//
// 备注 internal/worktree/（agent.go + create.go + changes.go +
// cleanup.go + notice.go + validate.go + setup.go 的合并简化版）。
// 差异：ref 解析直接走 git 子进程（Code 的 filesystem 直读优化不移植）；
// 不做全局 session 状态（session.go 是 CLI/TUI 专属，kcode 只有 agent 级
// worktree）；配置目录 .Code → .kcode。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// --- git 子进程 helper ---

// runGit 在 dir 下执行 git 命令，返回 stdout、stderr 与退出码（不返回 error，
// 调用方按 code 分支处理，与 Code 语义一致）。
func runGit(ctx context.Context, dir string, args ...string) (string, string, int) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return stdout.String(), stderr.String(), code
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// FindCanonicalGitRoot 返回仓库根的绝对路径；不在 git 仓库里返回 ""。
func FindCanonicalGitRoot(cwd string) string {
	stdout, _, code := runGit(context.Background(), cwd, "rev-parse", "--show-toplevel")
	if code != 0 {
		return ""
	}
	return trimNewline(stdout)
}

// --- slug 校验与命名 ---

// MaxWorktreeSlugLength caps the worktree name at 64 characters.
const MaxWorktreeSlugLength = 64

var validWorktreeSlugSegment = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateWorktreeSlug 防路径穿越与目录逃逸：slug 会经 filepath.Join 拼进
// `.kcode/worktrees/<slug>`，`./x`、`..`、绝对路径都会指到目录之外。允许
// `user/feature` 形态的嵌套（逐段校验），`/` 之后由 FlattenSlug 折叠。
func ValidateWorktreeSlug(slug string) error {
	if len(slug) > MaxWorktreeSlugLength {
		return fmt.Errorf("Invalid worktree name: must be %d characters or fewer (got %d)",
			MaxWorktreeSlugLength, len(slug))
	}
	for _, segment := range strings.Split(slug, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("Invalid worktree name %q: must not contain \".\" or \"..\" path segments", slug)
		}
		if !validWorktreeSlugSegment.MatchString(segment) {
			return fmt.Errorf(`Invalid worktree name %q: each "/"-separated segment must be non-empty and contain only letters, digits, dots, underscores, and dashes`, slug)
		}
	}
	return nil
}

// FlattenSlug 把嵌套 slug 折叠成单层名字（`user/feature` → `user+feature`）。
// `+` 在 git 分支名与文件路径里都合法，但不在 slug 段的允许字符集里，映射
// 因此是单射的。
func FlattenSlug(slug string) string {
	return strings.ReplaceAll(slug, "/", "+")
}

// WorktreeBranchName 返回 slug 对应的 git 分支名："worktree-<flattened>"。
func WorktreeBranchName(slug string) string {
	return "worktree-" + FlattenSlug(slug)
}

// WorktreesDir 是所有 kcode 管理的 worktree 的存放目录。
func WorktreesDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".kcode", "worktrees")
}

// WorktreePathFor 返回 slug 的 worktree 目录路径。
func WorktreePathFor(repoRoot, slug string) string {
	return filepath.Join(WorktreesDir(repoRoot), FlattenSlug(slug))
}

// --- 创建 ---

// CreateResult 是 getOrCreateWorktree 的结果。Existed=true 表示复用了已有
// worktree（跳过 `git worktree add` 与创建后设置）。
type CreateResult struct {
	WorktreePath   string
	WorktreeBranch string
	HeadCommit     string
	BaseBranch     string // Existed=true 时为空
	Existed        bool
}

// getOrCreateWorktree 在 <repoRoot>/.kcode/worktrees/ 下为 slug 创建 git
// worktree，已存在则快速恢复。
func getOrCreateWorktree(ctx context.Context, repoRoot, slug string) (*CreateResult, error) {
	worktreePath := WorktreePathFor(repoRoot, slug)
	worktreeBranch := WorktreeBranchName(slug)

	// 快速恢复：已存在 → 跳过 fetch 与 add。
	if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err == nil {
		stdout, _, code := runGit(ctx, worktreePath, "rev-parse", "HEAD")
		if code == 0 {
			return &CreateResult{
				WorktreePath:   worktreePath,
				WorktreeBranch: worktreeBranch,
				HeadCommit:     trimNewline(stdout),
				Existed:        true,
			}, nil
		}
	}

	if err := os.MkdirAll(WorktreesDir(repoRoot), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir worktrees dir: %w", err)
	}

	defaultBranch := GetDefaultBranch(ctx, repoRoot)

	// origin/<default> 本地已知就跳过 fetch（大仓库一次 fetch 要烧 6-8s 扫
	// commit-graph）。base 略旧没关系，用户在 worktree 里可以手动 pull。
	var baseBranch, baseSha string
	if stdout, _, code := runGit(ctx, repoRoot, "rev-parse", "--verify", "origin/"+defaultBranch); code == 0 {
		baseBranch = "origin/" + defaultBranch
		baseSha = trimNewline(stdout)
	} else {
		// 本地不认识 origin/<default> —— 试一次 fetch；失败（离线/没有
		// remote）就退回 HEAD，至少从当前工作树的 commit 开枝，不报错。
		if _, _, fetchCode := runGit(ctx, repoRoot, "fetch", "origin", defaultBranch); fetchCode == 0 {
			baseBranch = "origin/" + defaultBranch
		} else {
			baseBranch = "HEAD"
		}
		stdout, _, shaCode := runGit(ctx, repoRoot, "rev-parse", baseBranch)
		if shaCode != 0 {
			return nil, fmt.Errorf(`failed to resolve base branch %q: git rev-parse failed`, baseBranch)
		}
		baseSha = trimNewline(stdout)
	}

	// `-B`（大写）会重置此前删目录留下的孤儿分支；`-b` 会直接报错。
	_, stderr, code := runGit(ctx, repoRoot, "worktree", "add", "-B", worktreeBranch, worktreePath, baseBranch)
	if code != 0 {
		return nil, fmt.Errorf("failed to create worktree: %s", stderr)
	}

	return &CreateResult{
		WorktreePath:   worktreePath,
		WorktreeBranch: worktreeBranch,
		HeadCommit:     baseSha,
		BaseBranch:     baseBranch,
		Existed:        false,
	}, nil
}

// GetDefaultBranch 探测 origin 的默认分支：symbolic-ref → main/master 存在性
// → 当前分支 → "main"。
func GetDefaultBranch(ctx context.Context, repoRoot string) string {
	if stdout, _, code := runGit(ctx, repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); code == 0 {
		branch := trimNewline(stdout)
		if rest, ok := strings.CutPrefix(branch, "origin/"); ok {
			return rest
		}
		return branch
	}
	for _, candidate := range []string{"main", "master"} {
		if _, _, code := runGit(ctx, repoRoot, "rev-parse", "--verify", "origin/"+candidate); code == 0 {
			return candidate
		}
	}
	if stdout, _, code := runGit(ctx, repoRoot, "branch", "--show-current"); code == 0 {
		if b := trimNewline(stdout); b != "" {
			return b
		}
	}
	return "main"
}

// performPostCreationSetup 把主仓库的本地配置传播到新建 worktree：
// A. .kcode 本地设置文件；B. 共享 git hooks 目录。
func performPostCreationSetup(ctx context.Context, repoRoot, worktreePath string) {
	// A. 复制项目级本地配置（可能含本地凭据）。
	for _, rel := range []string{
		filepath.Join(".kcode", "settings.local.json"),
		filepath.Join(".kcode", "config.local.yaml"),
	} {
		src := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			continue // ENOENT 正常 —— 没有本地配置
		}
		dst := filepath.Join(worktreePath, rel)
		if os.MkdirAll(filepath.Dir(dst), 0o755) == nil {
			_ = os.WriteFile(dst, data, 0o644)
		}
	}

	// B. 指向主仓库的 hooks 目录，git hooks 行为保持一致（优先 .husky）。
	for _, candidate := range []string{
		filepath.Join(repoRoot, ".husky"),
		filepath.Join(repoRoot, ".git", "hooks"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			runGit(ctx, worktreePath, "config", "core.hooksPath", candidate)
			break
		}
	}
}

// --- Agent 级 API（agents/ 包通过 WorktreeHooks 消费）---

// AgentWorktreeResult 是 CreateAgentWorktree 的结果。
type AgentWorktreeResult struct {
	WorktreePath   string
	WorktreeBranch string
	HeadCommit     string
	GitRoot        string
}

// CreateAgentWorktree 为子 Agent 创建一个轻量 worktree。与会话级创建不同，
// 它完全不碰全局状态（不 chdir、不记录 session）。
func CreateAgentWorktree(ctx context.Context, slug string) (*AgentWorktreeResult, error) {
	if err := ValidateWorktreeSlug(slug); err != nil {
		return nil, err
	}

	cwd, _ := os.Getwd()
	gitRoot := FindCanonicalGitRoot(cwd)
	if gitRoot == "" {
		return nil, fmt.Errorf("cannot create agent worktree: not in a git repository")
	}

	result, err := getOrCreateWorktree(ctx, gitRoot, slug)
	if err != nil {
		return nil, err
	}

	if !result.Existed {
		performPostCreationSetup(ctx, gitRoot, result.WorktreePath)
	} else {
		// 抬 mtime，避免周期清理把刚复用的 worktree 判成过期。
		now := time.Now()
		_ = os.Chtimes(result.WorktreePath, now, now)
	}

	return &AgentWorktreeResult{
		WorktreePath:   result.WorktreePath,
		WorktreeBranch: result.WorktreeBranch,
		HeadCommit:     result.HeadCommit,
		GitRoot:        gitRoot,
	}, nil
}

// RemoveAgentWorktree 删除 CreateAgentWorktree 创建的 worktree 与分支。
// 返回是否成功。
func RemoveAgentWorktree(ctx context.Context, worktreePath, worktreeBranch, gitRoot string) bool {
	if gitRoot == "" {
		return false
	}

	if _, _, code := runGit(ctx, gitRoot, "worktree", "remove", "--force", worktreePath); code != 0 {
		return false
	}

	if worktreeBranch != "" {
		// 等 git 锁文件释放。
		time.Sleep(100 * time.Millisecond)
		runGit(ctx, gitRoot, "branch", "-D", worktreeBranch)
	}
	return true
}

// --- 变更检测 ---

// ChangeSummary 是 CountWorktreeChanges 的结果。
type ChangeSummary struct {
	ChangedFiles int
	Commits      int
}

// HasWorktreeChanges 返回 worktree 是否有未提交改动或 headCommit 之后的新
// commit。git 失败时返回 true（fail-closed）。
func HasWorktreeChanges(ctx context.Context, worktreePath, headCommit string) bool {
	stdout, _, code := runGit(ctx, worktreePath, "status", "--porcelain")
	if code != 0 {
		return true // fail-closed
	}
	if strings.TrimSpace(stdout) != "" {
		return true
	}

	stdout, _, code = runGit(ctx, worktreePath, "rev-list", "--count", headCommit+"..HEAD")
	if code != 0 {
		return true // fail-closed
	}
	n, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return true // fail-closed
	}
	return n > 0
}

// CountWorktreeChanges 返回明细变更统计；状态不可可靠判定时返回 nil。
// 把它当安全门用的调用方必须把 nil 当作"未知，按不安全处理"（fail-closed）。
func CountWorktreeChanges(ctx context.Context, worktreePath, originalHeadCommit string) *ChangeSummary {
	stdout, _, code := runGit(ctx, worktreePath, "status", "--porcelain")
	if code != 0 {
		return nil // fail-closed
	}
	changedFiles := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			changedFiles++
		}
	}

	if originalHeadCommit == "" {
		// 没有基线 commit 就没法数新 commit。fail-closed。
		return nil
	}

	stdout, _, code = runGit(ctx, worktreePath, "rev-list", "--count", originalHeadCommit+"..HEAD")
	if code != 0 {
		return nil
	}
	commits, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return nil
	}

	return &ChangeSummary{
		ChangedFiles: changedFiles,
		Commits:      commits,
	}
}

// --- 清理 ---

// ephemeralWorktreePatterns 识别可自动清理的一次性 worktree。用户命名的
// （如 "my-feature"）永远不会匹配。
var ephemeralWorktreePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^agent-a[0-9a-f]{7}$`),
	regexp.MustCompile(`^wf-[0-9a-f]{8}-[0-9a-f]{3}-\d+$`),
	regexp.MustCompile(`^wf-\d+$`),
	regexp.MustCompile(`^job-[a-zA-Z0-9._-]{1,55}-[0-9a-f]{8}$`),
}

func isEphemeralSlug(slug string) bool {
	for _, p := range ephemeralWorktreePatterns {
		if p.MatchString(slug) {
			return true
		}
	}
	return false
}

// CleanupStaleAgentWorktrees 删除早于 cutoffDate 的子 Agent/工作流 worktree。
// 三层安全过滤：名字模式、年龄、变更检查（fail-closed）。
// preservePath 是当前正在使用的 worktree（可为 ""），永远跳过。
func CleanupStaleAgentWorktrees(ctx context.Context, cutoffDate time.Time, preservePath string) int {
	cwd, _ := os.Getwd()
	gitRoot := FindCanonicalGitRoot(cwd)
	if gitRoot == "" {
		return 0
	}

	entries, err := os.ReadDir(WorktreesDir(gitRoot))
	if err != nil {
		return 0
	}

	removed := 0
	for _, entry := range entries {
		slug := entry.Name()

		// Layer 1: 只清一次性命名模式。
		if !isEphemeralSlug(slug) {
			continue
		}

		worktreePath := WorktreePathFor(gitRoot, slug)
		if preservePath == worktreePath {
			continue
		}

		// Layer 2: 年龄检查。
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoffDate) {
			continue
		}

		// Layer 3: fail-closed。-uno 跳过 untracked 扫描 —— 崩溃残留的
		// 构建产物不该挡住清理，大仓库上还能快 5-10 倍。
		statusOut, _, statusCode := runGit(ctx, worktreePath,
			"--no-optional-locks", "status", "--porcelain", "-uno")
		if statusCode != 0 || strings.TrimSpace(statusOut) != "" {
			continue
		}

		unpushedOut, _, unpushedCode := runGit(ctx, worktreePath,
			"rev-list", "--max-count=1", "HEAD", "--not", "--remotes")
		if unpushedCode != 0 || strings.TrimSpace(unpushedOut) != "" {
			continue
		}

		if RemoveAgentWorktree(ctx, worktreePath, WorktreeBranchName(slug), gitRoot) {
			removed++
		}
	}

	if removed > 0 {
		runGit(ctx, gitRoot, "worktree", "prune")
	}
	return removed
}

// --- 提示文本 ---

// BuildWorktreeNotice 生成注入子 Agent prompt 的迁移提示：告诉它继承的上下文
// 里的路径指向父工作目录，需要翻译到 worktree 根，并在编辑前重读文件。
func BuildWorktreeNotice(parentCwd, worktreeCwd string) string {
	return fmt.Sprintf(
		"You've inherited the conversation context above from a parent agent working in %s. "+
			"You are operating in an isolated git worktree at %s — same repository, same relative "+
			"file structure, separate working copy. Paths in the inherited context refer to the "+
			"parent's working directory; translate them to your worktree root. Re-read files before "+
			"editing if the parent may have modified them since they appear in the context. Your "+
			"changes stay in this worktree and will not affect the parent's files.",
		parentCwd, worktreeCwd,
	)
}
