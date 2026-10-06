package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Catalog Skill 内存注册表
//
// 阶段一加载只读 frontmatter（PromptBody 空，BodyLoaded false）；
// GetFull 触发阶段二读取 body（每次调用都重新读盘，实现热重载）。
type Catalog struct {
	skills      map[string]*Skill
	sources     map[string]string // skill name → "builtin" | "user" | "project"
	workDir     string
	dirModTimes map[string]time.Time
}

// NewCatalog 创建空 Catalog
func NewCatalog() *Catalog {
	return &Catalog{
		skills:      make(map[string]*Skill),
		sources:     make(map[string]string),
		dirModTimes: make(map[string]time.Time),
	}
}

// Register 注册或覆盖一个 skill
func (c *Catalog) Register(s *Skill, source string) {
	c.skills[s.Meta.Name] = s
	c.sources[s.Meta.Name] = source
}

// Get 返回阶段一 skill（frontmatter only，body 可能为空）
func (c *Catalog) Get(name string) *Skill {
	return c.skills[name]
}

// GetFull 返回 body 已加载的 skill（磁盘 skill 每次重新读盘）
func (c *Catalog) GetFull(name string) (*Skill, error) {
	skill, ok := c.skills[name]
	if !ok {
		return nil, fmt.Errorf("unknown skill: %s", name)
	}
	if skill.SourceDir == "" {
		// 内置 skill，body 已在内存
		return skill, nil
	}
	if err := loadSkillBody(skill); err != nil {
		if skill.PromptBody == "" {
			return nil, err
		}
		return skill, err
	}
	return skill, nil
}

// List 返回所有 skill 的元数据
func (c *Catalog) List() []SkillMeta {
	result := make([]SkillMeta, 0, len(c.skills))
	for _, s := range c.skills {
		result = append(result, s.Meta)
	}
	return result
}

// Source 返回 skill 来源标签
func (c *Catalog) Source(name string) string {
	return c.sources[name]
}

// Reload 重新扫描三层目录
func (c *Catalog) Reload(workDir string) {
	fresh := LoadCatalog(workDir)
	c.skills = fresh.skills
	c.sources = fresh.sources
	c.workDir = fresh.workDir
	c.dirModTimes = fresh.dirModTimes
}

// NeedsReload 检查 skill 目录的 modtime 是否变化
func (c *Catalog) NeedsReload() bool {
	for dir, recorded := range c.dirModTimes {
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
	dirs := skillDirPaths(c.workDir)
	for _, dir := range dirs {
		if _, tracked := c.dirModTimes[dir]; !tracked {
			if info, err := os.Stat(dir); err == nil && !info.ModTime().IsZero() {
				return true
			}
		}
	}
	return false
}

func (c *Catalog) snapshotDirModTimes() {
	c.dirModTimes = make(map[string]time.Time)
	for _, dir := range skillDirPaths(c.workDir) {
		info, err := os.Stat(dir)
		if err != nil {
			c.dirModTimes[dir] = time.Time{}
			continue
		}
		c.dirModTimes[dir] = info.ModTime()
	}
}

// skillDirPaths 返回用户级和项目级 skill 目录
func skillDirPaths(workDir string) []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".kcode", "skills"))
	}
	if workDir != "" {
		dirs = append(dirs, filepath.Join(workDir, ".kcode", "skills"))
	}
	return dirs
}

// LoadCatalog 三层合并加载：builtin → user → project（后者覆盖前者）
func LoadCatalog(workDir string) *Catalog {
	c := NewCatalog()
	c.workDir = workDir

	// Tier 1: 内置 skill
	for _, s := range LoadBuiltins() {
		c.Register(s, "builtin")
	}

	// Tier 2: 用户级 ~/.kcode/skills/
	if home, err := os.UserHomeDir(); err == nil {
		loadTierInto(c, filepath.Join(home, ".kcode", "skills"), "user")
	}

	// Tier 3: 项目级 .kcode/skills/
	loadTierInto(c, filepath.Join(workDir, ".kcode", "skills"), "project")

	c.snapshotDirModTimes()
	return c
}

// loadTierInto 扫描单层目录，注册每个子目录为阶段一 skill
func loadTierInto(c *Catalog, dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill, err := parseFrontmatterOnly(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		c.Register(skill, source)
	}
}
