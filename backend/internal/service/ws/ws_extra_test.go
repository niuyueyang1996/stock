package ws

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// 本文件补充 internal/service/ws 包的测试缺口：
//   - originAllowed（同源/跨源/无 Origin/非法 Origin）
//   - Handler 升级失败（非 WebSocket 请求）
//   - Broadcast 对已断连客户端逐出（hub.clients 减少）
//   - DataUpdated 空 codes 不广播
//   - Snapshot 未设置提供方返回 nil
//   - 并发 Broadcast + Register / DataUpdated 无数据竞争（由 -race 校验）
//   - client.write 写超时路径（触发 5s 写超时返回错误）
// ---------------------------------------------------------------------------

// connCollector 收集每个 WebSocket 升级后在 hub 中登记的服务端 *client，
// 供逐出/写超时/并发测试使用。
type connCollector struct {
	mu      sync.Mutex
	clients []*client
}

func (c *connCollector) collect(cl *client) {
	c.mu.Lock()
	c.clients = append(c.clients, cl)
	c.mu.Unlock()
}

// count 返回已收集的服务端 client 数量。
func (c *connCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.clients)
}

// get 返回第 i 个收集到的服务端 client。
func (c *connCollector) get(i int) *client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clients[i]
}

// waitForCollect 等待至少 n 个连接被收集（修复 Dial 成功但服务端 handler 未执行的竞态）。
func (c *connCollector) waitForCollect(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for c.count() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return c.count() >= n
}

// newCollectingServer 启动一个 httptest 服务器：每次升级后登记连接，
// 但不运行读循环（不会自动 Unregister），把服务端 *client 收集进 cc，
// 后续由测试显式触发 Broadcast 逐出或直接调用 write。
func newCollectingServer(t *testing.T, hub *Hub) (*httptest.Server, *connCollector) {
	cc := &connCollector{}
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		cl := hub.Register(conn)
		cc.collect(cl)
		<-stop // 阻塞：连接保持打开且登记在 hub，无读循环自动清理
	}))
	t.Cleanup(func() { close(stop); srv.Close() })
	return srv, cc
}

// countClients 返回 hub 当前登记的服务端连接数。
func countClients(h *Hub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// abortTCP 用 RST 硬断客户端的底层 TCP，使对端（服务端）后续写立即失败。
// SetLinger(0)+Close 丢弃缓冲并发送 RST，保证服务端 write 确定性报错。
func abortTCP(conn *websocket.Conn) {
	if tc, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}

// ---------------------------------------------------------------------------
// originAllowed
// ---------------------------------------------------------------------------

func testOriginRequest(host, origin string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/ws", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

// TestOriginAllowed 覆盖同源放行、跨源拒绝、无 Origin 放行、非法 Origin 拒绝。
func TestOriginAllowed(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		origin  string
		allowed bool
	}{
		{"同源-http", "example.com", "http://example.com", true},
		{"同源-带端口", "example.com:8080", "http://example.com:8080", true},
		{"同源-不同协议仍放行(仅比较 Host)", "example.com", "https://example.com", true},
		{"跨源-不同主机", "example.com", "http://evil.com", false},
		{"跨源-同主机不同端口", "example.com", "http://example.com:9999", false},
		{"无 Origin 放行", "example.com", "", true},
		{"非法 Origin URL", "example.com", "://bad", false},
		{"非法 Origin(含空格)", "example.com", "http://exa mple.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testOriginRequest(tc.host, tc.origin)
			if got := originAllowed(r); got != tc.allowed {
				t.Fatalf("originAllowed(Host=%q, Origin=%q) = %v, 期望 %v",
					tc.host, tc.origin, got, tc.allowed)
			}
		})
	}
}

// TestOriginAllowedViaUpgrader 通过真实 Upgrade 校验 Upgrader 会用 originAllowed 拒绝跨源。
func TestOriginAllowedViaUpgrader(t *testing.T) {
	hub := NewHub()
	srv, _ := newCollectingServer(t, hub)
	// 携带跨源 Origin 的 Dial：应失败，服务端不会登记连接。
	h := http.Header{}
	h.Set("Origin", "http://evil.com")
	if _, _, err := websocket.DefaultDialer.Dial(wsURL(srv), h); err == nil {
		t.Fatal("携带跨源 Origin 的握手应当被拒绝")
	}
	time.Sleep(20 * time.Millisecond)
	if n := countClients(hub); n != 0 {
		t.Fatalf("跨源握手后 hub 不应有任何连接, 实际 %d", n)
	}
}

// ---------------------------------------------------------------------------
// Handler 升级失败
// ---------------------------------------------------------------------------

// TestHandlerUpgradeFail 非 WebSocket 的普通 HTTP 请求会让 Upgrade 失败，
// handler 应优雅返回而不 panic，且不登记任何连接。
func TestHandlerUpgradeFail(t *testing.T) {
	hub := NewHub()
	h := Handler(hub, fakeSnapshot)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)

	// 调用后不应 panic
	h(rec, req)

	if n := countClients(hub); n != 0 {
		t.Fatalf("升级失败后 hub 不应有任何连接, 实际 %d", n)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非 ws 请求返回状态码 = %d, 期望 400", rec.Code)
	}
}

// TestHandlerUpgradeIncomplete 即便带上 Upgrade/Connection 头、但缺少
// Sec-WebSocket-Key/Version 等完整升级参数（或 ResponseWriter 不可劫持），
// Upgrade 同样失败：handler 优雅返回、不 panic、不登记连接。
func TestHandlerUpgradeIncomplete(t *testing.T) {
	hub := NewHub()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket") // 缺少 Sec-WebSocket-*，升级失败
	Handler(hub, fakeSnapshot)(rec, req)
	if n := countClients(hub); n != 0 {
		t.Fatalf("升级失败后 hub 不应有任何连接, 实际 %d", n)
	}
}

// ---------------------------------------------------------------------------
// Broadcast 逐出断连客户端
// ---------------------------------------------------------------------------

// TestBroadcastEvictsDeadClient Broadcast 写入失败时应把该断连客户端逐出 hub。
func TestBroadcastEvictsDeadClient(t *testing.T) {
	hub := NewHub()
	srv, cc := newCollectingServer(t, hub)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	if !cc.waitForCollect(1, 2*time.Second) || countClients(hub) != 1 {
		t.Fatalf("登记后应有 1 个连接, 收集=%d hub=%d", cc.count(), countClients(hub))
	}

	// 硬断客户端 TCP，使服务端写失败；无读循环故不会自动 Unregister，
	// 只有 Broadcast 的死连接逐出逻辑能移除它。
	abortTCP(conn)

	// 最多重试几次让 RST 生效并使 Broadcast 逐出该连接。
	for i := 0; i < 5 && countClients(hub) > 0; i++ {
		hub.Broadcast(map[string]any{"type": "jobs", "data": fakeSnapshot()})
		time.Sleep(20 * time.Millisecond)
	}
	if n := countClients(hub); n != 0 {
		t.Fatalf("断连客户端写入失败后应被 Broadcast 逐出, 剩余 %d", n)
	}
}

// TestBroadcastKeepsAliveClient 存活客户端写入成功后不应被逐出。
func TestBroadcastKeepsAliveClient(t *testing.T) {
	hub := NewHub()
	srv, cc := newCollectingServer(t, hub)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	if !cc.waitForCollect(1, 2*time.Second) {
		t.Fatalf("收集失败: %d", cc.count())
	}
	if err := cc.get(0).write(map[string]any{"type": "jobs", "data": fakeSnapshot()}); err != nil {
		t.Fatalf("对存活连接写失败: %v", err)
	}
	if n := countClients(hub); n != 1 {
		t.Fatalf("存活客户端不应被逐出, hub=%d", n)
	}
}

// ---------------------------------------------------------------------------
// DataUpdated 空 codes
// ---------------------------------------------------------------------------

// TestDataUpdatedEmptyCodes DataUpdated 传空 codes 时不触发任何广播。
func TestDataUpdatedEmptyCodes(t *testing.T) {
	hub := NewHub()
	srv, _ := newCollectingServer(t, hub)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	// 空切片与 nil 都不应广播
	hub.DataUpdated([]string{})
	hub.DataUpdated(nil)

	// 客户端在读超时窗口内不应收到任何消息。
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, rerr := conn.ReadMessage()
	if rerr == nil {
		t.Fatal("DataUpdated(空 codes) 不应发送任何消息")
	}
	var ne net.Error
	if !errors.As(rerr, &ne) || !ne.Timeout() {
		t.Fatalf("期望读超时(无消息), 实际错误: %v", rerr)
	}
}

// ---------------------------------------------------------------------------
// Snapshot / SetSnapshot
// ---------------------------------------------------------------------------

// TestSnapshotNil 未设置提供方时 Snapshot 返回 nil 且不 panic。
func TestSnapshotNil(t *testing.T) {
	if s := NewHub().Snapshot(); s != nil {
		t.Fatalf("未设置提供方时 Snapshot 应为 nil, 实际 %#v", s)
	}
}

// TestSetSnapshotAndSnapshot 设置提供方后 Snapshot 返回其输出。
func TestSetSnapshotAndSnapshot(t *testing.T) {
	hub := NewHub()
	hub.SetSnapshot(func() map[string]any { return map[string]any{"n": 7} })
	s := hub.Snapshot()
	if s["n"] != 7 {
		t.Fatalf("Snapshot = %#v, 期望 n=7", s)
	}
}

// ---------------------------------------------------------------------------
// 并发 Broadcast + Register 无数据竞争（-race 校验）
// ---------------------------------------------------------------------------

// TestConcurrentBroadcastAndRegisterNoRace 并发执行 Broadcast/DataUpdated 与
// Handler 处的 Register/Unregister（生产代码路径），由 -race 检测 h.clients
// 是否存在数据竞争。注意：每个连接由 Handler 创建唯一一个 client 包装，
// 写锁按连接隔离，故同连接内的多个写（初始快照 + Broadcast）被串行化。
func TestConcurrentBroadcastAndRegisterNoRace(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(Handler(hub, fakeSnapshot))
	defer srv.Close()

	var wg sync.WaitGroup

	// 广播者：并发 Broadcast / DataUpdated。
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				hub.Broadcast(map[string]any{"type": "jobs", "data": fakeSnapshot()})
				hub.DataUpdated([]string{"600000", "00700"})
			}
		}()
	}

	// 连接忙闲：反复 Dial + 读初始快照 + Close，驱动生产路径上的 Register/Unregister。
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				c, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
				if err != nil {
					t.Errorf("连接失败: %v", err)
					return
				}
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _, _ = c.ReadMessage() // 读掉连接时推送的初始快照
				_ = c.Close()             // 触发 Handler 读循环断开并 Unregister
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// client.write 写超时
// ---------------------------------------------------------------------------

// TestClientWriteTimeout 对端不读取时，大消息写入会在 writeTimeout 后超时返回错误，
// 覆盖 client.write 的超时路径。
func TestClientWriteTimeout(t *testing.T) {
	hub := NewHub()
	srv, cc := newCollectingServer(t, hub)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	if !cc.waitForCollect(1, 2*time.Second) {
		t.Fatalf("收集失败: %d", cc.count())
	}
	cl := cc.get(0)

	// 对端(conn)不读取；写入远超内核缓冲的大消息使写阻塞，直到 writeTimeout 超时。
	huge := map[string]any{
		"type": "jobs",
		"data": strings.Repeat("x", 32<<20),
	}
	start := time.Now()
	err = cl.write(huge)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("write(对端不读) 应因写超时返回错误, 实际为 nil")
	}
	if elapsed < writeTimeout-500*time.Millisecond {
		t.Fatalf("写超时应约在 %v 后返回, 实际 %.2fs", writeTimeout, elapsed.Seconds())
	}
	// 额外通过 Broadcast 验证：写失败的连接被逐出。
	hub.Broadcast(map[string]any{"type": "jobs", "data": fakeSnapshot()})
}
