package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// defaultHookTimeout hook 未设 timeout 时的默认上限
const DefaultHookTimeout = 10 * time.Minute

// EventName 钩子事件名
type EventName string

const (
	// --- 会话级（整个 Agent 生命周期只触发一次）---
	EventSessionStart EventName = "session_start" // Agent 启动时（BeforeAgent）
	EventSessionEnd   EventName = "session_end"   // Agent 结束时（AfterAgent）
	EventShutdown     EventName = "shutdown"      // 程序关闭时

	// --- 对话级（每轮 LLM 调用各触发一次）---
	EventPreSend     EventName = "pre_send"     // 发给 LLM 之前（BeforeModel，注入 prompt 生效）
	EventPostReceive EventName = "post_receive" // 收到 LLM 回复后（AfterModel，注入 prompt 下一轮生效）
	EventTurnStart   EventName = "turn_start"   // 一轮迭代开始（同 BeforeModel）
	EventTurnEnd     EventName = "turn_end"     // 一轮迭代结束（同 AfterModel）

	// --- 工具级（每次工具调用触发）---
	EventPreToolUse  EventName = "pre_tool_use"  // 工具执行前（可 reject 阻断）
	EventPostToolUse EventName = "post_tool_use" // 工具执行后（仅通知，不阻断）
)

// ActionType 动作类型
type ActionType string

const (
	ActionCommand ActionType = "command" // 执行 shell 命令
	ActionPrompt  ActionType = "prompt"  // 注入提示消息
	ActionHTTP    ActionType = "http"    // 发送 HTTP 请求
	ActionAgent   ActionType = "agent"   // 调用子 Agent
)

// Action 钩子动作
type Action struct {
	Type    ActionType        `yaml:"type"`
	Command string            `yaml:"command"`
	Message string            `yaml:"message"`
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Headers map[string]string `yaml:"headers"`
	Body    string            `yaml:"body"`
	Timeout time.Duration     `yaml:"timeout"`
}

// Hook 单条钩子配置
type Hook struct {
	ID        string    `yaml:"id"`
	Event     EventName `yaml:"event"`
	Condition string    `yaml:"if"`
	Action    Action    `yaml:"action"`
	Reject    bool      `yaml:"reject"`   // 是否阻断执行
	Once      bool      `yaml:"once"`     // 只触发一次
	Async     bool      `yaml:"async"`    // 异步执行
	OnError   string    `yaml:"on_error"` // fail / ignore / reject
}

// HookContext 钩子执行上下文
type HookContext struct {
	EventName EventName
	ToolName  string
	ToolArgs  map[string]any
	FilePath  string
	Message   string
	Error     string
}

// HookResult 钩子执行结果
type HookResult struct {
	HookID  string
	Output  string
	Success bool
	Reject  bool
}

// validEventNames 合法事件名白名单
var validEventNames = map[EventName]bool{
	EventSessionStart: true,
	EventSessionEnd:   true,
	EventTurnStart:    true,
	EventTurnEnd:      true,
	EventPreSend:      true,
	EventPostReceive:  true,
	EventPreToolUse:   true,
	EventPostToolUse:  true,
	EventShutdown:     true,
}

// Validate 校验钩子配置，返回所有错误的聚合
func Validate(hooks []Hook) error {
	var errs []error
	for i, h := range hooks {
		label := h.ID
		if label == "" {
			label = fmt.Sprintf("hook[%d]", i)
		} else {
			label = fmt.Sprintf("hook[%d] (id=%q)", i, h.ID)
		}

		if !validEventNames[h.Event] {
			errs = append(errs, fmt.Errorf("%s: unknown event %q", label, h.Event))
		}
		if h.Action.Timeout < 0 {
			errs = append(errs, fmt.Errorf("%s: action.timeout must be >= 0 (got %s)", label, h.Action.Timeout))
		}

		switch h.Action.Type {
		case ActionCommand:
			if strings.TrimSpace(h.Action.Command) == "" {
				errs = append(errs, fmt.Errorf("%s: action.command must be non-empty for type %q", label, h.Action.Type))
			}
		case ActionPrompt:
			if strings.TrimSpace(h.Action.Message) == "" {
				errs = append(errs, fmt.Errorf("%s: action.message must be non-empty for type %q", label, h.Action.Type))
			}
		case ActionHTTP:
			if strings.TrimSpace(h.Action.URL) == "" {
				errs = append(errs, fmt.Errorf("%s: action.url must be non-empty for type %q", label, h.Action.Type))
			} else {
				u, err := url.Parse(h.Action.URL)
				if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
					errs = append(errs, fmt.Errorf("%s: action.url must be a valid http(s) URL (got %q)", label, h.Action.URL))
				}
			}
		case ActionAgent:
			if strings.TrimSpace(h.Action.Message) == "" && strings.TrimSpace(h.Action.Command) == "" {
				errs = append(errs, fmt.Errorf("%s: action.message (or action.command) must be non-empty for type %q", label, h.Action.Type))
			}
		case "":
			errs = append(errs, fmt.Errorf("%s: action.type is required", label))
		default:
			errs = append(errs, fmt.Errorf("%s: unknown action.type %q", label, h.Action.Type))
		}
	}
	return errors.Join(errs...)
}

// --- 动作执行（纯函数） ---

// ExecuteAction 执行单个 hook 动作，返回结果
func ExecuteAction(h Hook, ctx HookContext) HookResult {
	switch h.Action.Type {
	case ActionCommand:
		return runCommand(h, ctx)
	case ActionPrompt:
		return HookResult{HookID: h.ID, Output: h.Action.Message, Success: true, Reject: h.Reject}
	case ActionHTTP:
		return runHTTP(h, ctx)
	case ActionAgent:
		return HookResult{HookID: h.ID, Output: "agent-type hook is not supported in CLI mode", Success: false, Reject: h.Reject}
	default:
		return HookResult{HookID: h.ID, Output: fmt.Sprintf("unknown action type: %s", h.Action.Type), Success: false}
	}
}

func runCommand(h Hook, ctx HookContext) HookResult {
	timeout := h.Action.Timeout
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	execCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "bash", "-c", h.Action.Command)
	cmd.Env = append(cmd.Environ(),
		"KCODE_EVENT="+string(ctx.EventName),
		"KCODE_TOOL="+ctx.ToolName,
		"KCODE_FILE_PATH="+ctx.FilePath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := strings.TrimSpace(stdout.String() + "\n" + stderr.String())

	if execCtx.Err() == context.DeadlineExceeded {
		msg := fmt.Sprintf("command timed out after %s", timeout)
		if output != "" {
			msg += ": " + output
		}
		return HookResult{HookID: h.ID, Output: msg, Success: false, Reject: h.Reject}
	}
	return HookResult{HookID: h.ID, Output: output, Success: err == nil, Reject: h.Reject}
}

func runHTTP(h Hook, ctx HookContext) HookResult {
	method := strings.ToUpper(h.Action.Method)
	if method == "" {
		method = "POST"
	}
	timeout := h.Action.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	body := h.Action.Body
	if body == "" {
		payload := map[string]any{
			"event": string(ctx.EventName), "tool": ctx.ToolName,
			"tool_args": ctx.ToolArgs, "file_path": ctx.FilePath,
			"message": ctx.Message, "error": ctx.Error,
		}
		if b, err := json.Marshal(payload); err == nil {
			body = string(b)
		}
	}

	cctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, h.Action.URL, strings.NewReader(body))
	if err != nil {
		return HookResult{HookID: h.ID, Output: err.Error(), Success: false, Reject: h.Reject}
	}
	for k, v := range h.Action.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" && body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return HookResult{HookID: h.ID, Output: err.Error(), Success: false, Reject: h.Reject}
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return HookResult{
		HookID:  h.ID,
		Output:  fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes))),
		Success: ok,
		Reject:  h.Reject,
	}
}

// --- 条件表达式求值 ---

// EvaluateCondition 评估 hook 条件表达式
func EvaluateCondition(condition string, ctx HookContext) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}
	if tokens := splitComposite(condition); len(tokens) > 1 {
		result := EvaluateCondition(tokens[0].expr, ctx)
		for i := 1; i < len(tokens); i++ {
			rhs := EvaluateCondition(tokens[i].expr, ctx)
			if tokens[i].op == "&&" {
				result = result && rhs
			} else {
				result = result || rhs
			}
		}
		return result
	}
	if strings.HasPrefix(condition, "!") {
		return !EvaluateCondition(strings.TrimSpace(condition[1:]), ctx)
	}
	return evaluateLeaf(condition, ctx)
}

type compToken struct {
	op   string
	expr string
}

func splitComposite(s string) []compToken {
	var out []compToken
	start := 0
	cur := ""
	for i := 0; i < len(s)-1; i++ {
		pair := s[i : i+2]
		if pair == "&&" || pair == "||" {
			out = append(out, compToken{op: cur, expr: strings.TrimSpace(s[start:i])})
			cur = pair
			start = i + 2
			i++
		}
	}
	out = append(out, compToken{op: cur, expr: strings.TrimSpace(s[start:])})
	if len(out) == 1 {
		return nil
	}
	return out
}

func evaluateLeaf(condition string, ctx HookContext) bool {
	for _, op := range []string{"!=", "=~", "=*", "=="} {
		if idx := strings.Index(condition, op); idx >= 0 {
			left := strings.TrimSpace(condition[:idx])
			right := strings.Trim(strings.TrimSpace(condition[idx+len(op):]), `"'`)
			val := ResolveVar(left, ctx)
			switch op {
			case "==":
				return val == right
			case "!=":
				return val != right
			case "=~":
				matched, _ := regexp.MatchString(strings.Trim(right, "/"), val)
				return matched
			case "=*":
				matched, _ := filepath.Match(right, val)
				return matched
			}
		}
	}
	return ResolveVar(condition, ctx) != ""
}

// ResolveVar 解析上下文变量（tool, event, file_path, message, args.*）
func ResolveVar(name string, ctx HookContext) string {
	switch name {
	case "tool":
		return ctx.ToolName
	case "event":
		return string(ctx.EventName)
	case "file_path":
		return ctx.FilePath
	case "message":
		return ctx.Message
	}
	if strings.HasPrefix(name, "args.") {
		key := strings.TrimPrefix(name, "args.")
		if ctx.ToolArgs != nil {
			if v, ok := ctx.ToolArgs[key]; ok {
				return fmt.Sprintf("%v", v)
			}
		}
	}
	return ""
}

/*

# 什么时候（EventName） × 做什么（ActionType）

- event: session_start          # Agent 启动时
  action:
    type: command               # 执行命令
    command: "notify '开始工作'"

- event: pre_send               # 发给 LLM 之前
  action:
    type: prompt                # 注入提醒
    message: "记得写测试"

- event: post_tool_use          # 工具执行后
  if: tool == "write_file"      # 条件：写文件时
  action:
    type: http                  # 发 webhook
    url: https://audit.example.com

- event: pre_tool_use           # 工具执行前
  if: args.command =~ "rm -rf"
  action:
    type: prompt                # 返回拒绝消息
    message: "不允许 rm -rf"
  reject: true                  # 阻断执行
*/
