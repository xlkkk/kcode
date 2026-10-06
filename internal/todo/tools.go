// tools.go — 任务板 Eino InvokableTool 四件套：TodoCreate / TodoUpdate /
// TodoList / TodoGet，供主 Agent、子 Agent 与 Teammates 协调使用。
//
// 备注 internal/todo/tools.go（Tool 接口重写为 Eino tool.InvokableTool）

package todo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// --- TodoCreate ---

type TaskCreateTool struct {
	List *TaskList
}

var _ tool.InvokableTool = &TaskCreateTool{}

func NewTaskCreateTool(list *TaskList) *TaskCreateTool { return &TaskCreateTool{List: list} }

func (t *TaskCreateTool) Name() string { return "TaskCreate" }

func (t *TaskCreateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.Name(),
		Desc: "Create a new task to track work. Use this to break complex work into smaller, trackable steps before starting implementation.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"subject": {
				Type:     schema.String,
				Desc:     "A brief title for the task",
				Required: true,
			},
			"description": {
				Type:     schema.String,
				Desc:     "What needs to be done",
				Required: true,
			},
			"activeForm": {
				Type: schema.String,
				Desc: "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")",
			},
			"metadata": {
				Type: schema.Object,
				Desc: "Arbitrary metadata to attach to the task",
			},
		}),
	}, nil
}

func (t *TaskCreateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		Subject     string         `json:"subject"`
		Description string         `json:"description"`
		ActiveForm  string         `json:"activeForm"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.Subject == "" || args.Description == "" {
		return "Error: subject and description are required", nil
	}

	task, err := t.List.Create(args.Subject, args.Description, args.ActiveForm, args.Metadata)
	if err != nil {
		return fmt.Sprintf("Error creating task: %s", err), nil
	}
	return fmt.Sprintf("Task #%s created successfully: %s", task.ID, task.Subject), nil
}

// --- TodoGet ---

type TaskGetTool struct {
	List *TaskList
}

var _ tool.InvokableTool = &TaskGetTool{}

func NewTaskGetTool(list *TaskList) *TaskGetTool { return &TaskGetTool{List: list} }

func (t *TaskGetTool) Name() string { return "TaskGet" }

func (t *TaskGetTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.Name(),
		Desc: "Get the details of a specific task by its ID.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"taskId": {
				Type:     schema.String,
				Desc:     "The ID of the task to retrieve",
				Required: true,
			},
		}),
	}, nil
}

func (t *TaskGetTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	if args.TaskID == "" {
		return "Error: taskId is required", nil
	}

	task, err := t.List.Get(args.TaskID)
	if err != nil {
		return fmt.Sprintf("Error: %s", err), nil
	}
	if task == nil {
		return fmt.Sprintf("Task #%s not found", args.TaskID), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Task #%s\n", task.ID)
	fmt.Fprintf(&sb, "Subject: %s\n", task.Subject)
	fmt.Fprintf(&sb, "Status: %s\n", task.Status)
	fmt.Fprintf(&sb, "Description: %s\n", task.Description)
	if len(task.Blocks) > 0 {
		fmt.Fprintf(&sb, "Blocks: %s\n", strings.Join(task.Blocks, ", "))
	}
	if len(task.BlockedBy) > 0 {
		fmt.Fprintf(&sb, "Blocked by: %s\n", strings.Join(task.BlockedBy, ", "))
	}
	if task.Owner != "" {
		fmt.Fprintf(&sb, "Owner: %s\n", task.Owner)
	}
	return sb.String(), nil
}

// --- TodoList ---

type TaskListTool struct {
	List *TaskList
}

var _ tool.InvokableTool = &TaskListTool{}

func NewTaskListTool(list *TaskList) *TaskListTool { return &TaskListTool{List: list} }

func (t *TaskListTool) Name() string { return "TaskList" }

func (t *TaskListTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.Name(),
		Desc:        "List all tasks in the current task list. Shows ID, status, subject, and blocking info.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *TaskListTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	tasks, err := t.List.List()
	if err != nil {
		return fmt.Sprintf("Error: %s", err), nil
	}
	if len(tasks) == 0 {
		return "No tasks found.", nil
	}

	completedIDs := make(map[string]bool)
	for _, task := range tasks {
		if task.Status == StatusCompleted {
			completedIDs[task.ID] = true
		}
	}

	var sb strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&sb, "#%s [%s] %s", task.ID, task.Status, task.Subject)
		if task.Owner != "" {
			fmt.Fprintf(&sb, " (owner: %s)", task.Owner)
		}
		var activeBlockers []string
		for _, b := range task.BlockedBy {
			if !completedIDs[b] {
				activeBlockers = append(activeBlockers, b)
			}
		}
		if len(activeBlockers) > 0 {
			fmt.Fprintf(&sb, " [blocked by: %s]", strings.Join(activeBlockers, ", "))
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// --- TodoUpdate ---

type TaskUpdateTool struct {
	List *TaskList
}

var _ tool.InvokableTool = &TaskUpdateTool{}

func NewTaskUpdateTool(list *TaskList) *TaskUpdateTool { return &TaskUpdateTool{List: list} }

func (t *TaskUpdateTool) Name() string { return "TaskUpdate" }

func (t *TaskUpdateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.Name(),
		Desc: "Update a task's status, subject, description, or dependencies. Set status to \"in_progress\" when starting work, \"completed\" when done. Set status to \"deleted\" to remove a task.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"taskId": {
				Type:     schema.String,
				Desc:     "The ID of the task to update",
				Required: true,
			},
			"subject": {
				Type: schema.String,
				Desc: "New subject for the task",
			},
			"description": {
				Type: schema.String,
				Desc: "New description for the task",
			},
			"activeForm": {
				Type: schema.String,
				Desc: "Present continuous form shown in spinner when in_progress",
			},
			"status": {
				Type:     schema.String,
				Desc:     "New status for the task",
				Enum:     []string{"pending", "in_progress", "completed", "deleted"},
				Required: false,
			},
			"addBlocks": {
				Type:     schema.Array,
				Desc:     "Task IDs that this task blocks",
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
			},
			"addBlockedBy": {
				Type:     schema.Array,
				Desc:     "Task IDs that block this task",
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
			},
			"owner": {
				Type: schema.String,
				Desc: "New owner for the task",
			},
			"metadata": {
				Type: schema.Object,
				Desc: "Metadata keys to merge. Set a key to null to delete it.",
			},
		}),
	}, nil
}

func (t *TaskUpdateTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}
	taskID, _ := args["taskId"].(string)
	if taskID == "" {
		return "Error: taskId is required", nil
	}

	task, changed, err := t.List.Update(taskID, args)
	if err != nil {
		return fmt.Sprintf("Error: %s", err), nil
	}
	if task == nil {
		return fmt.Sprintf("Error: task #%s not found", taskID), nil
	}
	if len(changed) == 0 {
		return fmt.Sprintf("Task #%s: no changes applied", taskID), nil
	}
	return fmt.Sprintf("Task #%s updated: %s", taskID, strings.Join(changed, ", ")), nil
}

// RegisterAll 把四个任务板工具注册进注册表适配器。
func RegisterAll(register func(tool.BaseTool), list *TaskList) {
	register(NewTaskCreateTool(list))
	register(NewTaskGetTool(list))
	register(NewTaskListTool(list))
	register(NewTaskUpdateTool(list))
}
