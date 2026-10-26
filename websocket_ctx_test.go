package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsPeer starts a WebSocket server whose handler runs serve on the upgraded
// connection, and returns a client transport dialed to it. The handler is
// released before the server shuts down so httptest.Server.Close never waits
// on a handler that is deliberately blocked.
func wsPeer(t *testing.T, serve func(c *websocket.Conn, release <-chan struct{})) *WebSocketTransport {
	t.Helper()
	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		serve(c, release)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	transport := &WebSocketTransport{conn: conn}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// silentPeer holds the connection open and reads, but never sends a frame.
func silentPeer(c *websocket.Conn, _ <-chan struct{}) {
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

// stallingPeer reads the first byte of the client's write, reports that the
// write is under way, then stops reading with a small receive buffer, so the
// rest of a large write blocks. Signalling on the first byte keeps the tests
// independent of how long Send spends marshalling before it writes.
func stallingPeer(started chan<- struct{}) func(*websocket.Conn, <-chan struct{}) {
	return func(c *websocket.Conn, release <-chan struct{}) {
		raw := c.UnderlyingConn()
		if tcp, ok := raw.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(8 << 10)
		}
		var b [1]byte
		if _, err := io.ReadFull(raw, b[:]); err == nil {
			close(started)
		}
		<-release
	}
}

// stalledSend dials a stallingPeer with a small client send buffer and returns
// the transport, a request far larger than both buffers, and the channel that
// closes once the write has begun.
func stalledSend(t *testing.T) (*WebSocketTransport, *Message, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	transport := wsPeer(t, stallingPeer(started))
	if tcp, ok := transport.conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(8 << 10)
	}
	msg, err := NewRequest(1, "tools/call", map[string]string{"blob": strings.Repeat("x", 4<<20)})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return transport, msg, started
}

// waitErr runs fn in a goroutine and fails the test if it doesn't return
// within limit.
func waitErr(t *testing.T, limit time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("call did not return within %s; it ignored ctx", limit)
		return nil
	}
}

// Recv must honor cancellation as well as deadlines: a notification listener
// with a cancellable ctx used to block forever on a silent peer.
func TestWebSocketTransport_RecvHonorsCancellation(t *testing.T) {
	transport := wsPeer(t, silentPeer)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	err := waitErr(t, 3*time.Second, func() error {
		_, err := transport.Recv(ctx)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv error = %v; want it to wrap context.Canceled", err)
	}
	if !transport.isDead() {
		t.Error("a cancelled Recv leaves gorilla's read side broken; the connection must be marked dead")
	}
}

// A ctx that is already done must not touch the connection, so a caller that
// gives up early doesn't destroy a healthy shared connection.
func TestWebSocketTransport_AlreadyDoneCtxKeepsConnection(t *testing.T) {
	transport := wsPeer(t, func(c *websocket.Conn, _ <-chan struct{}) {
		// Echo every frame back.
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, data); err != nil {
				return
			}
		}
	})

	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.Recv(done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv on a done ctx = %v; want context.Canceled", err)
	}
	ping, err := NewRequest(1, "ping", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := transport.Send(done, ping); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send on a done ctx = %v; want context.Canceled", err)
	}
	if transport.isDead() {
		t.Fatal("a done ctx must not mark the connection dead")
	}

	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := transport.Send(ctx, ping); err != nil {
		t.Fatalf("Send after an early-returning call: %v", err)
	}
	if _, err := transport.Recv(ctx); err != nil {
		t.Fatalf("Recv after an early-returning call: %v", err)
	}
}

// Send used to ignore ctx entirely: against a peer that stopped reading, a
// write blocked forever once socket buffers filled.
func TestWebSocketTransport_SendHonorsCancellation(t *testing.T) {
	transport, msg, started := stalledSend(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		time.Sleep(50 * time.Millisecond) // let the write fill the buffers and block
		cancel()
	}()

	err := waitErr(t, 10*time.Second, func() error { return transport.Send(ctx, msg) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v; want it to wrap context.Canceled", err)
	}
	if !transport.isDead() {
		t.Fatal("a Send cancelled mid-frame must mark the connection dead")
	}

	// Bounded so a regression fails instead of blocking on the stalled peer.
	next, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := transport.Send(next, msg); !errors.Is(err, ErrTransportClosed) {
		t.Errorf("Send after a mid-frame cancel = %v; want ErrTransportClosed", err)
	}
}

func TestWebSocketTransport_SendHonorsDeadline(t *testing.T) {
	transport, msg, started := stalledSend(t)

	// Generous enough that marshalling (slow under -race) finishes first.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := waitErr(t, 10*time.Second, func() error { return transport.Send(ctx, msg) })
	select {
	case <-started:
	default:
		t.Fatalf("deadline expired before the write began (err=%v); raise the deadline", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send error = %v; want it to wrap context.DeadlineExceeded", err)
	}
	if !transport.isDead() {
		t.Error("a Send that hit its deadline must mark the connection dead")
	}
}

func TestWebSocketTransport_ClosedReturnsErrTransportClosed(t *testing.T) {
	transport := wsPeer(t, silentPeer)
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ping, err := NewRequest(1, "ping", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := transport.Send(context.Background(), ping); !errors.Is(err, ErrTransportClosed) {
		t.Errorf("Send after Close = %v; want ErrTransportClosed", err)
	}
	if _, err := transport.Recv(context.Background()); !errors.Is(err, ErrTransportClosed) {
		t.Errorf("Recv after Close = %v; want ErrTransportClosed", err)
	}
	// The text is unchanged, so callers that still match strings keep working.
	if ErrTransportClosed.Error() != "transport closed" {
		t.Errorf("ErrTransportClosed text = %q", ErrTransportClosed.Error())
	}
}
