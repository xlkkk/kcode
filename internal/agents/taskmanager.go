// taskmanager.go — 异步子 Agent 任务表与完成通知队列。
//
// 备注 internal/agents/subagent.go 的 TaskManager 部分。
// 差异：去掉 AdoptRunning（Code 事件 channel 专属），Eino 版由
// runAsync / runFork 在自己的 goroutine 里直接驱动状态机。

package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type Task struct {
	ID        string
	Name      string
	Status    TaskStatus
	Output    string
	Error     string
	CreatedAt time.Time
	DoneAt    time.Time
	Cancel    context.CancelFunc
}

type TaskManager struct {
	mu            sync.Mutex
	tasks         map[string]*Task
	nextID        int
	notifications []TaskNotification
}

type TaskNotification struct {
	TaskID string
	Name   string
	Status TaskStatus
	Output string
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks: make(map[string]*Task),
	}
}

func (tm *TaskManager) CreateTask(name string) string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.nextID++
	id := fmt.Sprintf("task_%d", tm.nextID)
	tm.tasks[id] = &Task{
		ID:        id,
		Name:      name,
		Status:    TaskPending,
		CreatedAt: time.Now(),
	}
	return id
}

func (tm *TaskManager) GetTask(id string) *Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t := tm.tasks[id]
	if t == nil {
		return nil
	}
	clone := *t
	return &clone
}

func (tm *TaskManager) ListTasks() []*Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	result := make([]*Task, 0, len(tm.tasks))
	for _, t := range tm.tasks {
		clone := *t
		result = append(result, &clone)
	}
	return result
}

func (tm *TaskManager) SetRunning(id string, cancel context.CancelFunc) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.tasks[id]; ok {
		t.Status = TaskRunning
		t.Cancel = cancel
	}
}

func (tm *TaskManager) SetCompleted(id, output string) {
	tm.mu.Lock()
	var n *TaskNotification
	if t, ok := tm.tasks[id]; ok {
		t.Status = TaskCompleted
		t.Output = output
		t.DoneAt = time.Now()
		n = &TaskNotification{TaskID: id, Name: t.Name, Status: TaskCompleted, Output: output}
	}
	tm.mu.Unlock()
	if n != nil {
		tm.push(*n)
	}
}

func (tm *TaskManager) SetFailed(id, errMsg string) {
	tm.mu.Lock()
	var n *TaskNotification
	if t, ok := tm.tasks[id]; ok {
		t.Status = TaskFailed
		t.Error = errMsg
		t.DoneAt = time.Now()
		n = &TaskNotification{TaskID: id, Name: t.Name, Status: TaskFailed, Output: errMsg}
	}
	tm.mu.Unlock()
	if n != nil {
		tm.push(*n)
	}
}

func (tm *TaskManager) push(n TaskNotification) {
	tm.mu.Lock()
	tm.notifications = append(tm.notifications, n)
	tm.mu.Unlock()
}

// DrainNotifications 取走并清空累积的完成通知（调用方把它们注入下一轮上下文）。
func (tm *TaskManager) DrainNotifications() []TaskNotification {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	n := tm.notifications
	tm.notifications = nil
	return n
}

func (tm *TaskManager) FindByName(name string) *Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, t := range tm.tasks {
		if t.Name == name || strings.HasPrefix(t.Name, name+":") {
			clone := *t
			return &clone
		}
	}
	return nil
}

func (tm *TaskManager) CancelTask(id string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.tasks[id]; ok && t.Cancel != nil {
		t.Cancel()
		t.Status = TaskCancelled
		t.DoneAt = time.Now()
		return true
	}
	return false
}
