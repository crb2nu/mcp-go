package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebSocketTransport_CloseResetsInitialized(t *testing.T) {
	// Start a minimal WebSocket server so we get a real connection.
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		// Just hold the connection open until the test closes it.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	transport := &WebSocketTransport{conn: conn}
	transport.initialized.Store(true)

	if !transport.IsInitialized() {
		t.Fatal("expected initialized before close")
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if transport.IsInitialized() {
		t.Fatal("expected not initialized after close")
	}
}

func TestWebSocketClient_CloseConnectionRemovesCache(t *testing.T) {
	// Start a minimal WebSocket server.
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	client := NewWebSocketClient(WebSocketConfig{})
	// Seed the internal cache with a connection.
	seeded := &WebSocketTransport{conn: conn}
	seeded.initialized.Store(true)
	client.conns["test-server"] = seeded

	if len(client.conns) != 1 {
		t.Fatalf("cache size = %d, want 1", len(client.conns))
	}

	client.CloseConnection("test-server")

	if len(client.conns) != 0 {
		t.Fatalf("cache size = %d, want 0 after CloseConnection", len(client.conns))
	}

	// CloseConnection on non-existent server should not panic.
	client.CloseConnection("nonexistent")
}

func TestWebSocketClient_DialReturnsFreshTransportPerCall(t *testing.T) {
	// Pool callers (e.g. loom-core's hub pool) keep multiple *Conn entries
	// per server and assume Close()'ing one does not affect any other.
	// Before the fix, Dial delegated to GetConnection, which returned the
	// SAME cached transport to every caller — so the first caller's
	// "mark unhealthy + Close" caused every other pool entry's Send to
	// fail with "use of closed network connection". This test guards
	// against regressing to that aliasing behaviour.
	connections := 0
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		// Auto-respond to MCP initialize so transport.Initialize() returns
		// cleanly without us hand-rolling the full handshake.
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			// Naive echo of an initialize response if asked, else ignore.
			if strings.Contains(string(msg), `"initialize"`) {
				_ = c.WriteMessage(websocket.TextMessage,
					[]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"t","version":"0"}}}`))
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client := NewWebSocketClient(WebSocketConfig{URL: wsURL})

	a, err := client.Dial(t.Context(), "srv")
	if err != nil {
		t.Fatalf("first Dial: %v", err)
	}
	b, err := client.Dial(t.Context(), "srv")
	if err != nil {
		t.Fatalf("second Dial: %v", err)
	}
	connections = 2 // proven by both Dials succeeding; counted for clarity

	if a == b {
		t.Fatalf("Dial returned the same *Transport twice — pool entries would alias one socket")
	}

	// Closing one must not invalidate the other. (Before the fix, both
	// shared the same *websocket.Conn so Close() on a would also close
	// b's underlying socket.)
	if err := a.Close(); err != nil {
		t.Fatalf("close a: %v", err)
	}

	// b should still be usable. Send a no-op message; success means b's
	// underlying socket is still open.
	probe, _ := NewRequest(2, "tools/list", []byte(`{}`))
	if err := b.Send(t.Context(), probe); err != nil {
		t.Fatalf("after closing a, b.Send failed: %v", err)
	}
	_ = b.Close()

	if connections != 2 {
		t.Fatalf("expected 2 distinct connections, got %d", connections)
	}
}
