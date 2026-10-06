// store.go — 工作流状态持久化。
//
// 状态落盘到项目 .kcode/workflow.json(项目级共享),会话中断后
// 重开仍在原阶段断点续跑。文件缺失或损坏时回退到初始状态。

package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store 管理工作流状态的读写,带锁保护并发访问。
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore 创建持久化存储。dir 是项目 workDir。
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// FilePath 返回状态文件路径(.kcode/workflow.json)。
func (s *Store) FilePath() string {
	return filepath.Join(s.dir, ".kcode", "workflow.json")
}

// Load 读取状态;文件不存在或损坏时返回初始状态(不报错,流程可从头开始)。
func (s *Store) Load() *WorkflowState {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.FilePath())
	if err != nil {
		return NewState()
	}
	var state WorkflowState
	if err := json.Unmarshal(data, &state); err != nil {
		return NewState()
	}
	if state.Artifacts == nil {
		state.Artifacts = make(map[Stage]string)
	}
	return &state
}

// Save 落盘状态。自动创建 .kcode/ 目录。
func (s *Store) Save(state *WorkflowState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.FilePath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.FilePath(), data, 0o644)
}
