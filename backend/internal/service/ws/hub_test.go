package ws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSnapshot 返回一个固定 jobs 快照，便于断言初始推送内容。
func fakeSnapshot() map[string]any {
	return map[string]any{"running": false, "pct": 0, "label": "测试"}
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestConnectInitialSnapshot 客户端连接即收到 {"type":"jobs","data":<快照>}
func TestConnectInitialSnapshot(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(Handler(hub, fakeSnapshot))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取初始快照失败: %v", err)
	}
	var init map[string]any
	if err := json.Unmarshal(msg, &init); err != nil {
		t.Fatalf("初始快照非法 JSON: %v", err)
	}
	if init["type"] != "jobs" {
		t.Fatalf("初始 type = %v, 应为 jobs", init["type"])
	}
	data, ok := init["data"].(map[string]any)
	if !ok {
		t.Fatalf("初始 data 缺少或类型错误: %v", init["data"])
	}
	if data["label"] != "测试" {
		t.Fatalf("初始快照 label = %v, 应为 测试", data["label"])
	}
}

// TestBroadcastAfterConnect Broadcast 后客户端收到 jobs 推送。
func TestBroadcastAfterConnect(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(Handler(hub, fakeSnapshot))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	// 先读掉初始快照
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("读取初始快照失败: %v", err)
	}

	hub.Broadcast(map[string]any{"type": "jobs", "data": map[string]any{"running": true, "pct": 50}})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取广播失败: %v", err)
	}
	var b map[string]any
	if err := json.Unmarshal(msg, &b); err != nil {
		t.Fatalf("广播消息非法 JSON: %v", err)
	}
	if b["type"] != "jobs" {
		t.Fatalf("广播 type = %v, 应为 jobs", b["type"])
	}
	d, _ := b["data"].(map[string]any)
	if d["pct"] != float64(50) {
		t.Fatalf("广播 data.pct = %v, 应为 50", d["pct"])
	}
}

// TestDataUpdated DataUpdated 广播 {"type":"data_updated","codes":[...]}。
func TestDataUpdated(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(Handler(hub, fakeSnapshot))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil { // 读掉初始快照
		t.Fatalf("读取初始快照失败: %v", err)
	}

	hub.DataUpdated([]string{"600000", "00700", "510300"})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取 data_updated 失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatalf("data_updated 非法 JSON: %v", err)
	}
	if m["type"] != "data_updated" {
		t.Fatalf("type = %v, 应为 data_updated", m["type"])
	}
	codes, _ := m["codes"].([]any)
	if len(codes) != 3 || codes[1] != "00700" {
		t.Fatalf("codes = %v, 应与传入一致", codes)
	}
}

// TestNoClientsNoOp 无客户端时 Broadcast/DataUpdated 为空操作（不 panic）。
func TestNoClientsNoOp(t *testing.T) {
	hub := NewHub()
	hub.Broadcast(map[string]any{"type": "jobs", "data": fakeSnapshot()})
	hub.DataUpdated([]string{"600000"})
}

// TestDisconnectUnregister 客户端断开后从 Hub 移除（再广播不 panic）。
func TestDisconnectUnregister(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(Handler(hub, fakeSnapshot))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil { // 读掉初始快照
		t.Fatalf("读取初始快照失败: %v", err)
	}
	conn.Close()
	// 等服务端读循环感知断开并清理
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		n := len(hub.clients)
		hub.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	hub.mu.Lock()
	n := len(hub.clients)
	hub.mu.Unlock()
	if n != 0 {
		t.Fatalf("断开后 Hub 仍残留 %d 个连接", n)
	}
	hub.Broadcast(map[string]any{"type": "jobs", "data": fakeSnapshot()})
}
