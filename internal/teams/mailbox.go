// mailbox.go — 团队文件信箱与消息协议。
//
// 备注 internal/teams/filemailbox.go + protocol.go
// 差异：去掉计划审批消息（kcode 不实现 plan 模式）；in-process -only 场景
// 下成员与 Lead 同进程，但保留文件落盘，让 UI / 调试能看到消息记录，
// 也与 Code 的目录结构保持一致。

package teams

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// lockAcquireTimeout 是等待文件锁的总时限。到点返回错误交给调用方处理，
	// 不能悄悄把这条消息扔掉。
	lockAcquireTimeout = 5 * time.Second
	// staleLockAge 超过这个时长的锁文件视为持有者已经崩溃，可以强行接管。
	staleLockAge = 10 * time.Second
	// maxLockBackoff 限制退避上限，避免高并发下越退越久。
	maxLockBackoff = 80 * time.Millisecond
)

// 结构化消息类型。都带 RequestID，应答原样带回，收发双方才能把
// 应答和请求对上号。
const (
	// MsgText 是普通文本消息，直接拼进队友下一轮的 prompt。
	MsgText = "text"
	// MsgShutdownRequest 由 Lead 发起，请队友收工。
	MsgShutdownRequest = "shutdown_request"
	// MsgShutdownResponse 是队友对关闭请求的答复，Approve 为 false 表示还没干完。
	MsgShutdownResponse = "shutdown_response"
)

// ShutdownPrefix marks a mailbox message as a request to terminate the teammate.
const ShutdownPrefix = "[shutdown]"

// FileMailBox 是按收件人分文件的信箱。
type FileMailBox struct {
	baseDir string
	// 同进程内的并发先用内存锁串行化，文件锁只是形式上的兜底
	// （kcode 只有 in-process 队员，没有跨进程场景）。
	mu sync.Mutex
}

// FileMailMessage 是一条信箱消息。Type/RequestID/Approve 供结构化消息使用，
// 普通文本消息留空。
type FileMailMessage struct {
	From      string `json:"from"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	Read      bool   `json:"read"`

	Type      string `json:"type,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Approve   *bool  `json:"approve,omitempty"`
}

// NewFileMailMessage 构造一条普通文本消息，时间戳按 RFC3339Nano 记。
func NewFileMailMessage(from, text string) FileMailMessage {
	return FileMailMessage{
		From:      from,
		Text:      text,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func NewFileMailBox(baseDir string) *FileMailBox {
	os.MkdirAll(baseDir, 0755)
	return &FileMailBox{baseDir: baseDir}
}

func (mb *FileMailBox) inboxPath(agentID string) string {
	return filepath.Join(mb.baseDir, sanitizeTeamName(agentID)+".json")
}

func (mb *FileMailBox) lockPath(agentID string) string {
	return filepath.Join(mb.baseDir, sanitizeTeamName(agentID)+".json.lock")
}

func (mb *FileMailBox) Send(recipient string, msg FileMailMessage) error {
	return mb.withLock(recipient, func(messages []FileMailMessage) ([]FileMailMessage, error) {
		msg.Read = false
		if msg.Timestamp == "" {
			msg.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		return append(messages, msg), nil
	})
}

func (mb *FileMailBox) ReadUnread(agentID string) ([]FileMailMessage, error) {
	messages, err := mb.readInbox(agentID)
	if err != nil {
		return nil, err
	}
	var unread []FileMailMessage
	for _, m := range messages {
		if !m.Read {
			unread = append(unread, m)
		}
	}
	return unread, nil
}

func (mb *FileMailBox) MarkAllRead(agentID string) error {
	return mb.withLock(agentID, func(messages []FileMailMessage) ([]FileMailMessage, error) {
		for i := range messages {
			messages[i].Read = true
		}
		return messages, nil
	})
}

// withLock 拿文件锁 → 重读信箱 → 应用变更 → 写回。
func (mb *FileMailBox) withLock(agentID string, fn func([]FileMailMessage) ([]FileMailMessage, error)) error {
	mb.mu.Lock()
	defer mb.mu.Unlock()

	lockFile := mb.lockPath(agentID)

	// 抢文件锁：退避时间指数增长并带抖动；总时限内抢不到就报错。
	var lockFd *os.File
	var err error
	deadline := time.Now().Add(lockAcquireTimeout)
	backoff := 5 * time.Millisecond
	for {
		lockFd, err = os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
		if info, statErr := os.Stat(lockFile); statErr == nil {
			if time.Since(info.ModTime()) > staleLockAge {
				os.Remove(lockFile)
				continue
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mailbox %s: 等待文件锁超过 %s，消息未写入", agentID, lockAcquireTimeout)
		}
		time.Sleep(backoff + time.Duration(mrand.Int63n(int64(backoff))))
		if backoff < maxLockBackoff {
			backoff *= 2
		}
	}
	lockFd.Close()
	defer os.Remove(lockFile)

	messages, _ := mb.readInbox(agentID)
	messages, err = fn(messages)
	if err != nil {
		return err
	}
	return mb.writeInbox(agentID, messages)
}

func (mb *FileMailBox) readInbox(agentID string) ([]FileMailMessage, error) {
	data, err := os.ReadFile(mb.inboxPath(agentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var messages []FileMailMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, nil
	}
	return messages, nil
}

func (mb *FileMailBox) writeInbox(agentID string, messages []FileMailMessage) error {
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mb.inboxPath(agentID), data, 0644)
}

// --- 消息协议 ---

// NewRequestID 生成请求标识。用随机串而不是自增序号，跨进程也不会撞。
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-0"
	}
	return "req-" + hex.EncodeToString(b[:])
}

// NewShutdownRequest 构造关闭请求。Text 里放原因，队友要拿它判断该不该同意。
func NewShutdownRequest(from, reason string) FileMailMessage {
	if reason == "" {
		reason = "team is wrapping up"
	}
	return newTyped(from, MsgShutdownRequest, NewRequestID(), fmt.Sprintf("[shutdown] %s", reason))
}

// NewShutdownResponse 构造队友对关闭请求的答复。
func NewShutdownResponse(from, requestID string, approve bool, reason string) FileMailMessage {
	m := newTyped(from, MsgShutdownResponse, requestID, reason)
	m.Approve = &approve
	return m
}

func newTyped(from, msgType, requestID, text string) FileMailMessage {
	m := NewFileMailMessage(from, text)
	m.Type = msgType
	m.RequestID = requestID
	return m
}

// IsShutdownRequest 判断消息是不是关闭请求。除看 Type 外也认
// "[shutdown]" 文本前缀：用户手动往信箱里塞一行也该管用。
func IsShutdownRequest(m FileMailMessage) bool {
	return m.Type == MsgShutdownRequest || strings.HasPrefix(strings.TrimSpace(m.Text), ShutdownPrefix)
}

// Approved 返回应答是否为同意。字段缺省时按不同意处理。
func (m FileMailMessage) Approved() bool {
	return m.Approve != nil && *m.Approve
}

// CreateIdleNotification builds the message a teammate sends to the lead after
// finishing a turn. The lead routes work by reading these.
func CreateIdleNotification(memberName, reason string) FileMailMessage {
	return NewFileMailMessage(memberName, fmt.Sprintf("[idle] %s (reason: %s)", memberName, reason))
}
