// loader.go — Agent 定义三层加载器（built-in → user → project）+ 热重载。
//
// 备注 internal/agents/loader.go
// 热重载沿用 skills.Catalog 的 dirModTimes 检测模式。

package agents

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type AgentLoader struct {
	mu      sync.Mutex
	workDir string
	agents  map[string]*AgentDefinition

	// FailedFiles 记录最近一次 LoadAll 中解析失败的定义文件，每条形如 "<path>: <reason>"。
	FailedFiles []string

	// ErrorWriter 接收解析失败的一行警告，默认 os.Stderr。
	ErrorWriter io.Writer

	dirModTimes map[string]time.Time
}

func NewAgentLoader(workDir string) *AgentLoader {
	l := &AgentLoader{
		workDir:     workDir,
		agents:      make(map[string]*AgentDefinition),
		ErrorWriter: os.Stderr,
		dirModTimes: make(map[string]time.Time),
	}
	_ = l.LoadAll()
	return l
}

// LoadAll 全量重载三层定义（线程安全）。
func (l *AgentLoader) LoadAll() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	agents := make(map[string]*AgentDefinition)
	failed := []string{}

	// Tier 1: 内置 specs，不读盘。
	for name, spec := range BuiltinSpecs {
		spec := spec
		agents[name] = &AgentDefinition{
			AgentType:       spec.Name,
			WhenToUse:       spec.Description,
			Tools:           spec.Tools,
			DisallowedTools: spec.DisallowedTools,
			Model:           spec.Model,
			MaxTurns:        spec.MaxTurns,
			PermissionMode:  spec.PermissionMode,
			SystemPrompt:    spec.SystemPromptOverride,
			Background:      spec.Background,
			Isolation:       spec.Isolation,
			InitialPrompt:   spec.InitialPrompt,
			OmitKcodeMd:     spec.OmitKcodeMd,
			Source:          "built-in",
		}
	}

	// Tier 2 / 3: 用户级与项目级目录，后者覆盖前者。
	home, _ := os.UserHomeDir()
	if home != "" {
		l.loadDir(agents, &failed, filepath.Join(home, ".kcode", "agents"), "user")
	}
	if l.workDir != "" {
		l.loadDir(agents, &failed, filepath.Join(l.workDir, ".kcode", "agents"), "project")
	}

	l.agents = agents
	l.FailedFiles = failed
	l.snapshotDirModTimes()
	return nil
}

// loadDir 扫描单层目录，解析失败的跳过并记录。
func (l *AgentLoader) loadDir(agents map[string]*AgentDefinition, failed *[]string, dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		def, err := ParseAgentFile(path)
		if err != nil {
			msg := fmt.Sprintf("%s: %v", path, err)
			*failed = append(*failed, msg)
			if l.ErrorWriter != nil {
				fmt.Fprintf(l.ErrorWriter, "[kcode] agent definition skipped — %s\n", msg)
			}
			continue
		}
		def.Source = source
		def.Filename = entry.Name()
		agents[def.AgentType] = def
	}
}

// Get 返回指定类型的定义，不存在返回 nil。
func (l *AgentLoader) Get(agentType string) *AgentDefinition {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agents[agentType]
}

// ListNames 返回全部定义名（排序稳定，方便拼 prompt）。
func (l *AgentLoader) ListNames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, 0, len(l.agents))
	for name := range l.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Reload 重新执行全量加载（供热重载调用方使用）。
func (l *AgentLoader) Reload() {
	_ = l.LoadAll()
}

// NeedsReload 检查 agents 目录的 mtime 是否变化（同 skills.Catalog 模式）。
func (l *AgentLoader) NeedsReload() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for dir, recorded := range l.dirModTimes {
		info, err := os.Stat(dir)
		if err != nil {
			if recorded.IsZero() {
				continue
			}
			return true
		}
		if !info.ModTime().Equal(recorded) {
			return true
		}
	}
	for _, dir := range agentDirPaths(l.workDir) {
		if _, tracked := l.dirModTimes[dir]; !tracked {
			if info, err := os.Stat(dir); err == nil && !info.ModTime().IsZero() {
				return true
			}
		}
	}
	return false
}

func (l *AgentLoader) snapshotDirModTimes() {
	l.dirModTimes = make(map[string]time.Time)
	for _, dir := range agentDirPaths(l.workDir) {
		info, err := os.Stat(dir)
		if err != nil {
			l.dirModTimes[dir] = time.Time{}
			continue
		}
		l.dirModTimes[dir] = info.ModTime()
	}
}

// agentDirPaths 返回用户级和项目级 agent 定义目录。
func agentDirPaths(workDir string) []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".kcode", "agents"))
	}
	if workDir != "" {
		dirs = append(dirs, filepath.Join(workDir, ".kcode", "agents"))
	}
	return dirs
}
