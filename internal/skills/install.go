package skills

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SkillSource 远程 skill 来源
type SkillSource struct {
	Owner    string
	Repo     string
	Ref      string // 分支或标签，默认 "main"
	Subpath  string // 仓库内路径
	Name     string // skill 名（Subpath 最后一段）
	Original string // 原始 URL
}

// ParseSkillURL 解析三种 URL 格式：
//  1. https://www.skills.sh/<owner>/<repo>/<skill-name>
//  2. https://github.com/<owner>/<repo>/tree/<ref>/<subpath>
//  3. https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<subpath>/SKILL.md
func ParseSkillURL(raw string) (*SkillSource, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http(s) URLs are supported")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	switch u.Host {
	case "www.skills.sh", "skills.sh":
		if len(parts) < 3 {
			return nil, fmt.Errorf("skills.sh URL must be /<owner>/<repo>/<skill-name>")
		}
		return &SkillSource{
			Owner:    parts[0],
			Repo:     parts[1],
			Ref:      "main",
			Subpath:  "skills/" + strings.Join(parts[2:], "/"),
			Name:     parts[len(parts)-1],
			Original: raw,
		}, nil

	case "github.com":
		if len(parts) < 5 || parts[2] != "tree" {
			return nil, fmt.Errorf("github.com URL must be /<owner>/<repo>/tree/<ref>/<subpath>")
		}
		sub := strings.Join(parts[4:], "/")
		return &SkillSource{
			Owner:    parts[0],
			Repo:     parts[1],
			Ref:      parts[3],
			Subpath:  sub,
			Name:     parts[len(parts)-1],
			Original: raw,
		}, nil

	case "raw.githubusercontent.com":
		if len(parts) < 4 {
			return nil, fmt.Errorf("raw.githubusercontent.com URL too short")
		}
		subParts := parts[3:]
		if last := subParts[len(subParts)-1]; strings.Contains(last, ".") {
			subParts = subParts[:len(subParts)-1]
		}
		if len(subParts) == 0 {
			return nil, fmt.Errorf("raw URL missing skill subpath")
		}
		return &SkillSource{
			Owner:    parts[0],
			Repo:     parts[1],
			Ref:      parts[2],
			Subpath:  strings.Join(subParts, "/"),
			Name:     subParts[len(subParts)-1],
			Original: raw,
		}, nil
	}
	return nil, fmt.Errorf("unsupported host %q (try skills.sh or github.com)", u.Host)
}

// 安装限制
const (
	maxFileSize       = 1 << 20 // 1 MiB
	maxTotalSize      = 8 << 20 // 8 MiB
	maxFileCount      = 64
	maxRecursionDepth = 4
	httpTimeout       = 30 * time.Second
)

type contentEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	Size        int    `json:"size"`
}

type fetcher struct {
	client  *http.Client
	apiBase string
}

func newFetcher() *fetcher {
	return &fetcher{
		client:  &http.Client{Timeout: httpTimeout},
		apiBase: "https://api.github.com",
	}
}

func (f *fetcher) listContents(src *SkillSource, subpath string) ([]contentEntry, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s",
		f.apiBase, src.Owner, src.Repo, subpath, url.QueryEscape(src.Ref))
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kcode-install-skill")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contents API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github API forbidden (rate-limited?): %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned %d for %s", resp.StatusCode, endpoint)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFileSize))
	if err != nil {
		return nil, fmt.Errorf("read contents response: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("github returned empty body")
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var entries []contentEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("parse dir listing: %w", err)
		}
		return entries, nil
	}
	var single contentEntry
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, fmt.Errorf("parse file metadata: %w", err)
	}
	return []contentEntry{single}, nil
}

func (f *fetcher) fetchBlob(e contentEntry) ([]byte, error) {
	if e.Size > maxFileSize {
		return nil, fmt.Errorf("file %s too large: %d bytes (max %d)", e.Path, e.Size, maxFileSize)
	}
	if e.Encoding == "base64" && e.Content != "" {
		clean := strings.ReplaceAll(e.Content, "\n", "")
		out, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, fmt.Errorf("decode base64 for %s: %w", e.Path, err)
		}
		return out, nil
	}
	if e.DownloadURL == "" {
		return nil, fmt.Errorf("no download_url for %s", e.Path)
	}
	req, _ := http.NewRequest("GET", e.DownloadURL, nil)
	req.Header.Set("User-Agent", "kcode-install-skill")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: status %d", e.DownloadURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFileSize))
}

// InstallReport 安装结果摘要
type InstallReport struct {
	SkillName  string
	TargetDir  string
	FileCount  int
	TotalBytes int64
}

// Install 下载 skill 到 installRoot/<name>/
func Install(src *SkillSource, installRoot string) (*InstallReport, error) {
	return installWith(newFetcher(), src, installRoot)
}

func installWith(f *fetcher, src *SkillSource, installRoot string) (*InstallReport, error) {
	if src == nil {
		return nil, fmt.Errorf("nil source")
	}
	if err := validateSkillName(src.Name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create install root: %w", err)
	}
	staging, err := os.MkdirTemp(installRoot, ".install-"+src.Name+"-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	cleanupStaging := func() { _ = os.RemoveAll(staging) }

	report := &InstallReport{SkillName: src.Name}
	if err := walkAndDownload(f, src, src.Subpath, staging, report, 0); err != nil {
		cleanupStaging()
		return nil, err
	}
	if !hasSkillManifest(staging) {
		cleanupStaging()
		return nil, fmt.Errorf("downloaded tree missing SKILL.md or skill.yaml")
	}

	final := filepath.Join(installRoot, src.Name)
	if _, err := os.Stat(final); err == nil {
		if err := os.RemoveAll(final); err != nil {
			cleanupStaging()
			return nil, fmt.Errorf("remove old install: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		cleanupStaging()
		return nil, fmt.Errorf("promote staging dir: %w", err)
	}
	report.TargetDir = final
	return report, nil
}

func walkAndDownload(f *fetcher, src *SkillSource, subpath, localDir string, report *InstallReport, depth int) error {
	if depth > maxRecursionDepth {
		return fmt.Errorf("install tree too deep (>%d levels)", maxRecursionDepth)
	}
	entries, err := f.listContents(src, subpath)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if report.FileCount >= maxFileCount {
			return fmt.Errorf("install file count limit (%d) reached", maxFileCount)
		}
		if strings.Contains(e.Name, "..") || strings.ContainsAny(e.Name, "/\\") {
			return fmt.Errorf("suspicious entry name: %q", e.Name)
		}
		target := filepath.Join(localDir, e.Name)
		switch e.Type {
		case "file":
			data, err := f.fetchBlob(e)
			if err != nil {
				return err
			}
			if int64(report.TotalBytes)+int64(len(data)) > maxTotalSize {
				return fmt.Errorf("install total size limit (%d bytes) reached", maxTotalSize)
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}
			report.FileCount++
			report.TotalBytes += int64(len(data))
		case "dir":
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := walkAndDownload(f, src, e.Path, target, report, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasSkillManifest(dir string) bool {
	for _, name := range []string{"SKILL.md", "skill.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func validateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("empty skill name")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("skill name cannot start with '.'")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("skill name %q contains invalid char %q (use a-z 0-9 - _)", name, r)
		}
	}
	return nil
}

// UserSkillsRoot 返回 ~/.kcode/skills，自动创建父目录
func UserSkillsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	root := filepath.Join(home, ".kcode", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}
