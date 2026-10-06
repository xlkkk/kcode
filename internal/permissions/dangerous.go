package permissions

import (
	"regexp"
	"strings"
)

// 危险命令检测 — 正则黑名单
// 备注 internal/permissions/permissions.go DetectDangerous()

// 危险命令黑名单
type dangerousPattern struct {
	re     *regexp.Regexp
	reason string
}

var defaultDangerousPatterns = []dangerousPattern{
	// 文件系统破坏
	{regexp.MustCompile(`rm\s+-[a-z]*r[a-z]*f[a-z]*\s+/\s*$`), "recursive force delete root"},
	{regexp.MustCompile(`mkfs\.`), "format disk"},
	{regexp.MustCompile(`dd\s+if=.*of=/dev/`), "direct write to disk device"},
	{regexp.MustCompile(`chmod\s+-R\s+777\s+/`), "recursive chmod root"},
	{regexp.MustCompile(`:\(\)\{\s*:\|:&\s*\};:`), "fork bomb"},
	// 远程脚本执行
	{regexp.MustCompile(`curl\s+.*\|\s*(ba)?sh`), "pipe remote script"},
	{regexp.MustCompile(`wget\s+.*\|\s*(ba)?sh`), "pipe remote script"},
	// 磁盘设备写入
	{regexp.MustCompile(`>\s*/dev/sd`), "overwrite disk device"},
	// Git 破坏性命令
	{regexp.MustCompile(`git\s+push\s+.*--force`), "force push"},
	{regexp.MustCompile(`git\s+reset\s+--hard`), "hard reset"},
	{regexp.MustCompile(`git\s+clean\s+-f`), "force clean untracked files"},
	{regexp.MustCompile(`git\s+checkout\s+\.`), "discard all changes"},
	{regexp.MustCompile(`git\s+branch\s+-D`), "force delete branch"},
}

// DetectDangerous 检查命令是否匹配危险模式
func DetectDangerous(command string) (bool, string) {
	for _, p := range defaultDangerousPatterns {
		if p.re.MatchString(command) {
			return true, p.reason
		}
	}
	return false, ""
}

// 安全只读命令白名单 — 不需要权限确认
var safeCommandPrefixes = []string{
	"ls", "dir", "pwd", "echo", "cat", "head", "tail", "wc",
	"find", "which", "whereis", "whoami", "hostname", "uname",
	"date", "cal", "uptime", "df", "du", "free", "env", "printenv",
	"file", "stat", "readlink", "realpath", "basename", "dirname",
	"sort", "uniq", "tr", "cut", "awk", "sed", "grep", "egrep", "fgrep",
	"diff", "comm", "tee", "xargs", "true", "false", "test",
	"git status", "git log", "git diff", "git show", "git branch",
	"git tag", "git remote", "git rev-parse", "git ls-files",
	"git blame", "git stash list", "go version", "go env",
	"node -v", "npm -v", "npx", "python --version", "pip list",
	"cargo --version", "rustc --version",
}

// IsSafeCommand 检查命令是否为安全的只读命令
func IsSafeCommand(command string) bool {
	cmd := strings.TrimSpace(command)
	for _, prefix := range safeCommandPrefixes {
		if cmd == prefix || strings.HasPrefix(cmd, prefix+" ") || strings.HasPrefix(cmd, prefix+"\t") {
			// 包含重定向、管道、复合命令的不算安全
			if !strings.Contains(cmd, ">") && !strings.Contains(cmd, "|") &&
				!strings.Contains(cmd, ";") && !strings.Contains(cmd, "&&") &&
				!strings.Contains(cmd, "$(") && !strings.Contains(cmd, "`") {
				return true
			}
		}
	}
	return false
}
