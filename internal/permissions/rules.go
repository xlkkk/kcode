package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// 规则引擎 — 三层 YAML 规则文件（用户级/项目级/本地级）
// 优先级：deny > ask > allow
// 备注 internal/permissions/permissions.go RuleEngine

// RuleEffect 规则效果
type RuleEffect string

const (
	RuleAllow RuleEffect = "allow"
	RuleDeny  RuleEffect = "deny"
	RuleAsk   RuleEffect = "ask"
)

// Rule 单条权限规则
type Rule struct {
	ToolName string     // 工具名（如 "bash"）
	Pattern  string     // 匹配模式（支持通配符 *）
	Effect   RuleEffect // 效果：allow/deny/ask
}

// Matches 检查规则是否匹配给定的工具和内容
func (r Rule) Matches(toolName, content string) bool {
	if r.ToolName != toolName {
		return false
	}
	return globMatch(r.Pattern, content)
}

// globMatch 通配符匹配，* 匹配任意字符（包括 /）
func globMatch(pattern, content string) bool {
	// 无通配符时精确比较
	if !strings.Contains(pattern, "*") && !strings.Contains(pattern, "?") {
		return pattern == content
	}
	// 将 pattern 转为正则
	re := "^"
	for _, ch := range pattern {
		switch ch {
		case '*':
			re += ".*"
		case '?':
			re += "."
		case '.', '+', '^', '$', '{', '}', '(', ')', '|', '[', ']', '\\':
			re += "\\" + string(ch)
		default:
			re += string(ch)
		}
	}
	re += "$"
	matched, _ := regexp.MatchString(re, content)
	return matched
}

// cachedRules 规则文件缓存
type cachedRules struct {
	modTime time.Time
	size    int64
	rules   []Rule
}

// RuleEngine 规则引擎
type RuleEngine struct {
	UserPath    string // 用户级规则文件（~/.kcode/permissions.yaml）
	ProjectPath string // 项目级规则文件（.kcode/permissions.yaml）
	LocalPath   string // 本地级规则文件（.kcode/permissions.local.yaml）

	mu    sync.Mutex
	cache map[string]cachedRules
}

// NewRuleEngine 创建规则引擎
func NewRuleEngine(workDir string) *RuleEngine {
	e := &RuleEngine{
		ProjectPath: filepath.Join(workDir, ".kcode", "permissions.yaml"),
		LocalPath:   filepath.Join(workDir, ".kcode", "permissions.local.yaml"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		e.UserPath = filepath.Join(home, ".kcode", "permissions.yaml")
	}
	return e
}

// Evaluate 评估规则，返回匹配的最严格效果
//
// 优先级 deny > ask > allow，没有任何规则命中时返回 nil。
func (e *RuleEngine) Evaluate(toolName, content string) *RuleEffect {
	return EvaluateRules(e.Snapshot(), toolName, content)
}

// Snapshot 获取所有规则文件的合并快照
//
// 文件没变动时复用缓存，变动了才重新解析。
func (e *RuleEngine) Snapshot() []Rule {
	var all []Rule
	for _, path := range []string{e.UserPath, e.ProjectPath, e.LocalPath} {
		all = append(all, e.rulesFor(path)...)
	}
	return all
}

// rulesFor 获取单个规则文件的规则（带缓存）
func (e *RuleEngine) rulesFor(path string) []Rule {
	if path == "" {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		e.mu.Lock()
		delete(e.cache, path)
		e.mu.Unlock()
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.cache[path]; ok && c.modTime.Equal(info.ModTime()) && c.size == info.Size() {
		return c.rules
	}

	rules := loadRulesFile(path)
	if e.cache == nil {
		e.cache = make(map[string]cachedRules)
	}
	e.cache[path] = cachedRules{modTime: info.ModTime(), size: info.Size(), rules: rules}
	return rules
}

// EvaluateRules 在给定规则集上裁决
//
// 优先级 deny > ask > allow，没有任何规则命中时返回 nil。
func EvaluateRules(rules []Rule, toolName, content string) *RuleEffect {
	var hit *RuleEffect
	for _, r := range rules {
		if !r.Matches(toolName, content) {
			continue
		}
		switch r.Effect {
		case RuleDeny:
			eff := RuleDeny
			return &eff
		case RuleAsk:
			eff := RuleAsk
			hit = &eff
		case RuleAllow:
			if hit == nil {
				eff := RuleAllow
				hit = &eff
			}
		}
	}
	return hit
}

// AppendLocalRule 追加本地规则
func (e *RuleEngine) AppendLocalRule(r Rule) {
	if e.LocalPath == "" {
		return
	}
	os.MkdirAll(filepath.Dir(e.LocalPath), 0o755)
	rules := loadRulesFile(e.LocalPath)
	rules = append(rules, r)
	var entries []map[string]string
	for _, rule := range rules {
		entries = append(entries, map[string]string{
			"rule":   fmt.Sprintf("%s(%s)", rule.ToolName, rule.Pattern),
			"effect": string(rule.Effect),
		})
	}
	data, _ := yaml.Marshal(entries)
	os.WriteFile(e.LocalPath, data, 0o644)
}

// loadRulesFile 加载规则文件
func loadRulesFile(path string) []Rule {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []struct {
		RuleStr string `yaml:"rule"`
		Effect  string `yaml:"effect"`
	}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil
	}
	var rules []Rule
	for _, e := range entries {
		if e.Effect != "allow" && e.Effect != "deny" && e.Effect != "ask" {
			continue
		}
		r, err := parseRule(e.RuleStr, RuleEffect(e.Effect))
		if err != nil {
			continue
		}
		rules = append(rules, r)
	}
	return rules
}

var ruleRE = regexp.MustCompile(`^(\w+)\((.+)\)$`)

// parseRule 解析规则字符串，格式: ToolName(pattern)
func parseRule(raw string, effect RuleEffect) (Rule, error) {
	m := ruleRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Rule{}, fmt.Errorf("invalid rule syntax: %s", raw)
	}
	return Rule{ToolName: m[1], Pattern: m[2], Effect: effect}, nil
}
