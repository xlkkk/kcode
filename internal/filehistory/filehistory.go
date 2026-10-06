// Package filehistory 提供文件快照与回滚能力。
//
// 每次 write_file / edit_file 执行前，由 FileTrackMW 自动调用 TrackEdit
// 备份文件当前内容；每轮对话结束可 MakeSnapshot 建立检查点；
// 用户可通过 Rewind 回滚到任意检查点。
//
// 备注 internal/filehistory/filehistory.go
package filehistory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxSnapshots = 100

// Backup 单个文件在某版本的备份位置
type Backup struct {
	BackupPath string    `json:"backup_path"`
	Version    int       `json:"version"`
	Time       time.Time `json:"time"`
}

// Snapshot 一次对话检查点：消息下标 + 当时的全部文件备份索引
type Snapshot struct {
	MessageIndex int               `json:"message_index"`
	UserText     string            `json:"user_text"`
	Backups      map[string]Backup `json:"backups"`
	Timestamp    time.Time         `json:"timestamp"`
}

// History 文件历史记录器（并发安全）
type History struct {
	mu           sync.Mutex
	sessionDir   string
	trackedFiles map[string]int // filepath → current version
	snapshots    []Snapshot
}

// New 创建 History，备份文件存放在 <baseDir>/.kcode/file-history/<sessionID>/
func New(baseDir, sessionID string) *History {
	dir := filepath.Join(baseDir, ".kcode", "file-history", sessionID)
	_ = os.MkdirAll(dir, 0o755)
	return &History{
		sessionDir:   dir,
		trackedFiles: make(map[string]int),
	}
}

func backupName(filePath string, version int) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("%x@v%d", h[:8], version)
}

// TrackEdit 在文件被修改前备份当前内容。
// 文件不存在（新建文件）时不产生备份，但仍记录版本，
// 使 Rewind 知道该版本文件不存在、回滚时应删除。
func (h *History) TrackEdit(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	ver := h.trackedFiles[absPath]
	newVer := ver + 1

	data, err := os.ReadFile(absPath)
	if err == nil {
		bp := filepath.Join(h.sessionDir, backupName(absPath, newVer))
		_ = os.WriteFile(bp, data, 0o644)
	}

	h.trackedFiles[absPath] = newVer
}

// MakeSnapshot 为当前对话轮建立检查点。userText 是 UI 展示用的简短标签。
func (h *History) MakeSnapshot(msgIndex int, userText string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	backups := make(map[string]Backup, len(h.trackedFiles))
	for path, ver := range h.trackedFiles {
		bp := filepath.Join(h.sessionDir, backupName(path, ver))
		if _, err := os.Stat(bp); err != nil {
			if data, readErr := os.ReadFile(path); readErr == nil {
				_ = os.WriteFile(bp, data, 0o644)
			}
		}
		backups[path] = Backup{BackupPath: bp, Version: ver, Time: time.Now()}
	}

	snap := Snapshot{
		MessageIndex: msgIndex,
		UserText:     userText,
		Backups:      backups,
		Timestamp:    time.Now(),
	}

	h.snapshots = append(h.snapshots, snap)
	if len(h.snapshots) > maxSnapshots {
		h.snapshots = h.snapshots[len(h.snapshots)-maxSnapshots:]
	}
}

// GetSnapshots 返回全部快照的副本，供 UI 展示。
func (h *History) GetSnapshots() []Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Snapshot, len(h.snapshots))
	copy(out, h.snapshots)
	return out
}

// Rewind 回滚到指定快照时刻的文件状态，返回实际被改动的文件列表。
func (h *History) Rewind(snapshotIndex int) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if snapshotIndex < 0 || snapshotIndex >= len(h.snapshots) {
		return nil, fmt.Errorf("invalid snapshot index %d", snapshotIndex)
	}

	target := h.snapshots[snapshotIndex]
	var changed []string

	for path, backup := range target.Backups {
		backupData, err := os.ReadFile(backup.BackupPath)
		if err != nil {
			// 备份缺失 → 该时刻文件不存在，删除当前文件
			if _, statErr := os.Stat(path); statErr == nil {
				_ = os.Remove(path)
				changed = append(changed, path)
			}
			continue
		}

		currentData, _ := os.ReadFile(path)
		if string(currentData) != string(backupData) {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			if writeErr := os.WriteFile(path, backupData, 0o644); writeErr == nil {
				changed = append(changed, path)
			}
		}
	}

	// 截断目标之后的快照
	h.snapshots = h.snapshots[:snapshotIndex+1]

	// 重置跟踪文件的版本到快照版本
	for path, backup := range target.Backups {
		h.trackedFiles[path] = backup.Version
	}

	return changed, nil
}

// HasSnapshots 是否至少有一个可回滚的检查点。
func (h *History) HasSnapshots() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.snapshots) > 0
}

// Save 将快照元数据持久化到磁盘。
func (h *History) Save() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.MarshalIndent(h.snapshots, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.sessionDir, "snapshots.json"), data, 0o644)
}
