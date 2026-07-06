package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestWebSocketTransport_RecvHonorsContextDeadline is the regression for the
// Mills spin-wedge: a stalled hub (connection open, no reply) must not hang the
// caller past its context deadline. gorilla's ReadMessage blocks forever on a
// silent peer, so Recv derives a read deadline from ctx. Without the fix this
// blocks until the 3s guard and fails.
func TestWebSocketTransport_RecvHonorsContextDeadline(t *testing.T) {
	// A server that upgrades and then stays SILENT — never sends a frame, holds
	// the connection open by reading. Models a hub that accepted the connection
	// but stopped responding.
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
	transport := &WebSocketTransport{conn: conn}
	defer func() { _ = transport.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, rerr := transport.Recv(ctx)
		done <- rerr
	}()

	select {
	case rerr := <-done:
		if rerr == nil {
			t.Fatal("Recv returned nil error on a silent peer; want a deadline/timeout error")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("Recv took %s to honor a 150ms deadline", elapsed)
		}
		// The stalled connection must be marked dead so the pool redials.
		if !transport.isDead() {
			t.Error("a deadline-expired Recv should mark the connection dead")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Recv did not return within 3s — it ignored the context deadline (regression)")
	}
}

// TestWebSocketTransport_RecvNoDeadlineStillReads verifies the fix doesn't break
// the ordinary path: a ctx without a deadline clears any stale read deadline and
// a message that DOES arrive is received normally.
func TestWebSocketTransport_RecvNoDeadlineStillReads(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		// Wait for the client's first read to be pending, then reply once.
		time.Sleep(20 * time.Millisecond)
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
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
	defer func() { _ = transport.Close() }()

	msg, err := transport.Recv(context.Background()) // no deadline
	if err != nil {
		t.Fatalf("Recv with no deadline should read the reply, got: %v", err)
	}
	if msg == nil {
		t.Fatal("Recv returned nil message")
	}
}
