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
		defer c.Close()
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

	transport := &WebSocketTransport{
		conn:        conn,
		initialized: true,
	}

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
		defer c.Close()
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
	client.conns["test-server"] = &WebSocketTransport{
		conn:        conn,
		initialized: true,
	}

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
