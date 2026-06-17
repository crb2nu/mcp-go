package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeMCPServer is a minimal WebSocket MCP endpoint for transport-durability
// tests. It completes the initialize handshake, counts upgrades (dials) and
// inbound pings, and otherwise holds the connection open.
type fakeMCPServer struct {
	srv      *httptest.Server
	url      string
	upgrades atomic.Int32
	pings    atomic.Int32
}

func newFakeMCPServer(t *testing.T) *fakeMCPServer {
	t.Helper()
	f := &fakeMCPServer{}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.upgrades.Add(1)
		c.SetPingHandler(func(appData string) error {
			f.pings.Add(1)
			return c.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})
		defer func() { _ = c.Close() }()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]interface{}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			if method, _ := msg["method"].(string); method == "initialize" {
				resp, _ := json.Marshal(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"protocolVersion": ProtocolVersion,
						"capabilities":    map[string]interface{}{},
						"serverInfo":      map[string]interface{}{"name": "fake", "version": "1.0.0"},
					},
				})
				if err := c.WriteMessage(websocket.TextMessage, resp); err != nil {
					return
				}
			}
			// notifications/initialized and anything else: no reply.
		}
	}))
	f.url = "ws" + strings.TrimPrefix(f.srv.URL, "http")
	t.Cleanup(f.srv.Close)
	return f
}

// A live cached connection is reused without redialing.
func TestGetConnection_ReusesLiveConnection(t *testing.T) {
	f := newFakeMCPServer(t)
	client := NewWebSocketClient(WebSocketConfig{URL: f.url, KeepAliveInterval: -1, IdleProbeThreshold: -1})
	ctx := context.Background()

	c1, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get1: %v", err)
	}
	c2, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if c1 != c2 {
		t.Fatal("expected the same cached connection")
	}
	if got := f.upgrades.Load(); got != 1 {
		t.Fatalf("upgrades=%d, want 1", got)
	}
}

// Keepalive pings an otherwise-idle connection so the upstream proxy does not
// reap it (the close-1006 storm in .loom/149).
func TestKeepalive_PingsIdleConnection(t *testing.T) {
	f := newFakeMCPServer(t)
	client := NewWebSocketClient(WebSocketConfig{URL: f.url, KeepAliveInterval: 40 * time.Millisecond, IdleProbeThreshold: -1})
	ctx := context.Background()

	conn, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// No application traffic for several keepalive intervals.
	time.Sleep(250 * time.Millisecond)

	if conn.isDead() {
		t.Fatal("connection marked dead despite a healthy server")
	}
	if got := f.pings.Load(); got < 2 {
		t.Fatalf("pings=%d, want >=2 (keepalive not firing)", got)
	}
	if got := f.upgrades.Load(); got != 1 {
		t.Fatalf("upgrades=%d, want 1 (no redial expected)", got)
	}
}

// A single upstream close that marks the pooled connection dead triggers exactly
// one redial even when the whole pool reconnects concurrently (singleflight).
func TestReconnect_SingleflightCollapsesHerd(t *testing.T) {
	f := newFakeMCPServer(t)
	client := NewWebSocketClient(WebSocketConfig{URL: f.url, KeepAliveInterval: -1, IdleProbeThreshold: -1})
	ctx := context.Background()

	if _, err := client.GetConnection(ctx, "srv"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Simulate an upstream close detected by the pool.
	client.mu.Lock()
	client.conns["srv"].markDead()
	client.mu.Unlock()

	const n = 25
	var wg sync.WaitGroup
	start := make(chan struct{})
	conns := make([]*WebSocketTransport, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			conns[i], errs[i] = client.GetConnection(ctx, "srv")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if conns[i] == nil || !conns[i].IsInitialized() {
			t.Fatalf("caller %d got an unusable connection", i)
		}
	}
	if got := f.upgrades.Load(); got != 2 {
		t.Fatalf("upgrades=%d, want 2 (1 seed + 1 collapsed redial)", got)
	}
}

// A dead cached connection is never handed out; GetConnection evicts and redials.
func TestGetConnection_EvictsDeadConnection(t *testing.T) {
	f := newFakeMCPServer(t)
	client := NewWebSocketClient(WebSocketConfig{URL: f.url, KeepAliveInterval: -1, IdleProbeThreshold: -1})
	ctx := context.Background()

	c1, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get1: %v", err)
	}
	c1.markDead()

	c2, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if c2 == c1 {
		t.Fatal("expected a fresh connection after the cached one died")
	}
	if !c2.IsInitialized() {
		t.Fatal("fresh connection not initialized")
	}
	if got := f.upgrades.Load(); got != 2 {
		t.Fatalf("upgrades=%d, want 2", got)
	}
}

// A healthy connection idle beyond the probe threshold is liveness-pinged and
// then reused (no needless redial).
func TestGetConnection_IdleProbePassesForHealthyConn(t *testing.T) {
	f := newFakeMCPServer(t)
	client := NewWebSocketClient(WebSocketConfig{URL: f.url, KeepAliveInterval: -1, IdleProbeThreshold: 10 * time.Millisecond})
	ctx := context.Background()

	c1, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get1: %v", err)
	}
	time.Sleep(30 * time.Millisecond) // exceed the idle probe threshold

	c2, err := client.GetConnection(ctx, "srv")
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if c1 != c2 {
		t.Fatal("a healthy idle connection should be reused after a successful probe")
	}
	if got := f.upgrades.Load(); got != 1 {
		t.Fatalf("upgrades=%d, want 1", got)
	}
	// The probe ping is written synchronously, but the server processes the
	// control frame from its read loop asynchronously — poll briefly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for f.pings.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.pings.Load() < 1 {
		t.Fatal("expected a synchronous idle-probe ping")
	}
}
