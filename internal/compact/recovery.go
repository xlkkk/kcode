package compact

import "sync"

// 恢复状态
// - RecoveryState: 文件快照 + Skill 快照（并发安全）
// - BuildRecoveryAttachment: 压缩后附加恢复信息
// 备注 internal/compact/recovery.go

// FileSnapshot 记录某次文件读取的内容
type FileSnapshot struct {
	Path    string
	Content string
}

// RecoveryState 管理文件快照，用于压缩后恢复上下文
type RecoveryState struct {
	mu        sync.RWMutex
	snapshots map[string]string // path → content
}

// NewRecoveryState 创建恢复状态
func NewRecoveryState() *RecoveryState {
	return &RecoveryState{
		snapshots: make(map[string]string),
	}
}

// RecordFileRead 记录一次文件读取快照
func (s *RecoveryState) RecordFileRead(path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[path] = content
}

// GetSnapshot 获取指定路径的文件快照
func (s *RecoveryState) GetSnapshot(path string) (FileSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	content, ok := s.snapshots[path]
	if !ok {
		return FileSnapshot{}, false
	}
	return FileSnapshot{Path: path, Content: content}, true
}

// GetAllSnapshots 获取所有文件快照
func (s *RecoveryState) GetAllSnapshots() []FileSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]FileSnapshot, 0, len(s.snapshots))
	for path, content := range s.snapshots {
		result = append(result, FileSnapshot{Path: path, Content: content})
	}
	return result
}
