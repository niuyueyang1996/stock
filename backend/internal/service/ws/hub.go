// Package ws 提供 WebSocket 推送服务：连接管理 + 跨线程广播。
// 对齐 Python app/ws.py 协议：
//   - 连接即推一次 {"type":"jobs","data":<jobs 快照>}
//   - 任务进度/终态经 jobs.Manager.OnBroadcast 推送（0.3s 节流，终态 force 必推）
//   - 盘中动态刷新循环调 DataUpdated 广播 {"type":"data_updated","codes":[...]}
//
// 前端消费（static/js/common.js connectWs/wsOnMessage）：
//   - type=jobs → renderJobSnapshot(msg.data)
//   - type=data_updated → dispatchEvent CustomEvent('app-data-updated')
//
// 并发约束：gorilla 限制同一 Conn 同时只允许一个 reader 与一个 writer。
// 本包为每个连接维护一个写锁，保证「连接处初始快照」与「其它协程 Broadcast」
// 并发发生时写操作串行，符合 gorilla 规则。
package ws

import (
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeTimeout = 5 * time.Second // 单次写超时
	readLimit    = 1 << 20         // 1MB 读上限（客户端消息忽略，仅防异常大包）
)

// Upgrader 将 HTTP 升级为 WebSocket；CheckOrigin 校验同源。
var Upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return originAllowed(r) },
}

// originAllowed 校验 Origin 与请求 Host 相同（同源）。无 Origin（脚本/测试直连）放行。
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// client 包一层连接并提供写锁：多线程（连接处初始快照 + Broadcast）并发写时串行化。
type client struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// write 加锁写一条 JSON 消息（错误返回，由调用方决定是否逐出）。
func (c *client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer c.conn.SetWriteDeadline(time.Time{})
	return c.conn.WriteJSON(v)
}

// Hub 管理所有活跃 WebSocket 连接并广播消息。
type Hub struct {
	mu       sync.Mutex            // 保护 clients/snapshot
	clients  map[*client]struct{}  // 每个连接一个写锁包装
	snapshot func() map[string]any // jobs 快照提供方（可选）
}

// NewHub 创建空 Hub。
func NewHub() *Hub {
	return &Hub{clients: map[*client]struct{}{}}
}

// Register 登记一个 WebSocket 连接，返回其写锁包装后（供 Handler 使用）。
func (h *Hub) Register(conn *websocket.Conn) *client {
	cl := &client{conn: conn}
	h.mu.Lock()
	h.clients[cl] = struct{}{}
	n := len(h.clients)
	h.mu.Unlock()
	log.Printf("[ws] 连接建立 clients=%d", n)
	return cl
}

// Unregister 移除一个连接的写锁包装（等价退订）。幂等。
func (h *Hub) Unregister(cl *client) {
	h.mu.Lock()
	delete(h.clients, cl)
	n := len(h.clients)
	h.mu.Unlock()
	log.Printf("[ws] 连接断开 clients=%d", n)
}

// Broadcast 向所有连接广播消息；发送失败的断连客户端逐出。
// 无客户端时为空操作。
func (h *Hub) Broadcast(msg map[string]any) {
	h.mu.Lock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	if len(clients) == 0 {
		return
	}
	var dead []*client
	for _, c := range clients {
		if err := c.write(msg); err != nil {
			dead = append(dead, c)
		}
	}
	if len(dead) > 0 {
		h.mu.Lock()
		for _, c := range dead {
			delete(h.clients, c)
		}
		h.mu.Unlock()
	}
}

// SetSnapshot 设置 jobs 快照提供方（在 Handler 里调用；也供直接注册快照）。
func (h *Hub) SetSnapshot(fn func() map[string]any) {
	h.mu.Lock()
	h.snapshot = fn
	h.mu.Unlock()
}

// Snapshot 返回 jobs 快照（map），未设置提供方则返回 nil。
func (h *Hub) Snapshot() map[string]any {
	h.mu.Lock()
	fn := h.snapshot
	h.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// DataUpdated 广播盘中数据更新通知（对当前动态刷新涉及的代码）。
// 前端据此 dispatchEvent('app-data-updated') 自动重绘。
func (h *Hub) DataUpdated(codes []string) {
	if len(codes) == 0 {
		return
	}
	h.Broadcast(map[string]any{"type": "data_updated", "codes": codes})
}

// Handler 返回 WebSocket 处理器：升级连接 → 立即推初始 jobs 快照 →
// 循环读（忽略客户端消息、仅检测断开）→ 断线清理。
func Handler(hub *Hub, snapshot func() map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] 升级失败: %v", err)
			return
		}
		if snapshot != nil {
			hub.SetSnapshot(snapshot)
		}
		cl := hub.Register(conn)

		// 连接即推初始 jobs 快照（经 client.write 加锁，与 Broadcast 并发安全）
		if err := cl.write(map[string]any{"type": "jobs", "data": hub.Snapshot()}); err != nil {
			hub.Unregister(cl)
			_ = conn.Close()
			return
		}

		// 循环读：忽略客户端消息，仅靠它检测对端断开
		conn.SetReadLimit(readLimit)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
		hub.Unregister(cl)
		_ = conn.Close()
	}
}
