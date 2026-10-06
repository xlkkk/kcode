// store.go — 任务板 JSON 落盘（<dir>/.kcode/tasks/<listID>.json）。
//
// 备注 internal/todo/store.go

package todo

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Store struct {
	path string
}

func NewStore(dir, listID string) *Store {
	return &Store{
		path: filepath.Join(dir, ".kcode", "tasks", listID+".json"),
	}
}

func (s *Store) Load() ([]*Task, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var tasks []*Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) Save(tasks []*Task) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Delete 移除落盘文件；文件不存在不算错误。
func (s *Store) Delete() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
