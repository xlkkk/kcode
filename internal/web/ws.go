// ws.go — WebSocket 连接核心：升级、读写循环、心跳。
//
// 对应移植计划 Phase 7.4。每个连接一个 conn wrapper：
//   - writeCh 串行化所有下行写入（gorilla.Conn 不支持并发写）；
//   - 30s ping 心跳，读侧设 read limit + pong 保活；
//   - 上行消息解码成 Envelope 后交给 handler 回调。

package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1 << 20 // 1 MiB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// 本地开发服务，同源/本地文件页面都要能连；不做域名白名单。
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsConn 是一条已升级的 WebSocket 连接。
type wsConn struct {
	conn      *websocket.Conn
	send      chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newWSConn(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxMessageSize)
	_ = c.SetReadDeadline(time.Now().Add(pongWait))
	c.SetPongHandler(func(string) error {
		_ = c.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	wsc := &wsConn{
		conn:   c,
		send:   make(chan []byte, 64),
		closed: make(chan struct{}),
	}
	go wsc.writePump()
	return wsc, nil
}

// writePump 把 send 队列里的帧串行写向客户端。
func (w *wsConn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		w.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-w.send:
			_ = w.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Close 被调用：正常关闭
				_ = w.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := w.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = w.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := w.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-w.closed:
			_ = w.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = w.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}

// readLoop 阻塞读上行帧，每条 Envelope 交给 handler；返回即连接断开。
func (w *wsConn) readLoop(handler func(Envelope)) error {
	for {
		_, data, err := w.conn.ReadMessage()
		if err != nil {
			return err
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue // 坏帧直接忽略，不断连
		}
		handler(env)
	}
}

// sendJSON 把 v 编码成 {type, data} 信封投进写队列。队列满（客户端掉线
// 且未察觉）时丢弃，UI 事件是尽力而为，不能把 Agent 运行循环拖死。
func (w *wsConn) sendJSON(evtType string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	env, err := json.Marshal(Envelope{Type: evtType, Data: data})
	if err != nil {
		return
	}
	select {
	case w.send <- env:
	default:
	}
}

func (w *wsConn) sendRaw(env Envelope) {
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	select {
	case w.send <- data:
	default:
	}
}

// Close 关闭连接与写泵。幂等。
func (w *wsConn) Close() {
	w.closeOnce.Do(func() {
		close(w.closed)
	})
}
