package mcp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func TestStdioTransportRecv_IgnoresNonJSONLines(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, io.Discard)

	go func() {
		_, _ = io.WriteString(w, "debug: hello\n")
		_, _ = io.WriteString(w, "\n")
		_, _ = io.WriteString(w, "  \n")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := tr.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv() error: %v", err)
	}
	if msg == nil {
		t.Fatalf("Recv() returned nil message")
	}
	if msg.Method != "ping" {
		t.Fatalf("msg.Method = %q, want %q", msg.Method, "ping")
	}
	if msg.ID != float64(1) && msg.ID != 1 {
		t.Fatalf("msg.ID = %#v, want 1", msg.ID)
	}
}

// TestStdioTransportSendRecv_IDPreservation verifies that the transport
// preserves message IDs through a send/recv round-trip.
func TestStdioTransportSendRecv_IDPreservation(t *testing.T) {
	// Create a pipe pair where one side sends, other side receives.
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	t.Cleanup(func() { _ = r1.Close(); _ = w1.Close(); _ = r2.Close(); _ = w2.Close() })

	client := NewStdioTransport(r1, w2)
	server := NewStdioTransport(r2, w1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Client sends a request.
	req := &Message{
		JSONRPC: JSONRPCVersion,
		ID:      "test-42",
		Method:  "tools/call",
	}
	go func() {
		if err := client.Send(ctx, req); err != nil {
			t.Errorf("client Send: %v", err)
		}
	}()

	// Server receives and echoes back with same ID.
	srvMsg, err := server.Recv(ctx)
	if err != nil {
		t.Fatalf("server Recv: %v", err)
	}
	if fmt.Sprint(srvMsg.ID) != "test-42" {
		t.Fatalf("server received ID = %v, want test-42", srvMsg.ID)
	}

	resp := &Message{
		JSONRPC: JSONRPCVersion,
		ID:      srvMsg.ID,
		Result:  []byte(`{"ok":true}`),
	}
	go func() {
		if err := server.Send(ctx, resp); err != nil {
			t.Errorf("server Send: %v", err)
		}
	}()

	clientResp, err := client.Recv(ctx)
	if err != nil {
		t.Fatalf("client Recv: %v", err)
	}
	if fmt.Sprint(clientResp.ID) != "test-42" {
		t.Fatalf("client received ID = %v, want test-42", clientResp.ID)
	}
}

// TestStdioTransport_SharedReaderInterleaving demonstrates that a single
// StdioTransport shared by multiple callers delivers responses FIFO—callers
// get whatever message is next in the buffer, NOT necessarily the response
// matching their request. This documents the architectural limitation that
// the daemon's callLock exists to mitigate.
func TestStdioTransport_SharedReaderInterleaving(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	shared := NewStdioTransport(r, io.Discard)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Write two responses out of order: ID=2 first, then ID=1.
	go func() {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"order":"first"}}`+"\n")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"order":"second"}}`+"\n")
	}()

	// First Recv gets ID=2 (FIFO from buffer).
	msg1, err := shared.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 1: %v", err)
	}
	if fmt.Sprint(msg1.ID) != "2" {
		t.Fatalf("first Recv ID = %v, want 2 (FIFO)", msg1.ID)
	}

	// Second Recv gets ID=1.
	msg2, err := shared.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 2: %v", err)
	}
	if fmt.Sprint(msg2.ID) != "1" {
		t.Fatalf("second Recv ID = %v, want 1 (FIFO)", msg2.ID)
	}
}

// TestPipeTransport_IDCorrelation verifies that PipeTransport preserves IDs
// and can be used for testing ID matching logic.
func TestPipeTransport_IDCorrelation(t *testing.T) {
	client, server := NewPipeTransport()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ids := []any{"req-1", "req-2", "req-3"}
	go func() {
		for _, id := range ids {
			req := &Message{JSONRPC: JSONRPCVersion, ID: id, Method: "test"}
			_ = client.Send(ctx, req)
		}
	}()

	for _, expectedID := range ids {
		msg, err := server.Recv(ctx)
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if fmt.Sprint(msg.ID) != fmt.Sprint(expectedID) {
			t.Fatalf("received ID = %v, want %v", msg.ID, expectedID)
		}
	}
}

func TestStdioTransportRecv_Concurrent(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, io.Discard)

	const n = 50
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(n)

	errCh := make(chan error, n)
	got := make(chan int, n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			msg, err := tr.Recv(ctx)
			if err != nil {
				errCh <- err
				return
			}
			idNum, ok := msg.ID.(float64) // JSON numbers decode to float64
			if !ok {
				errCh <- fmt.Errorf("unexpected msg.ID type: %T (%#v)", msg.ID, msg.ID)
				return
			}
			got <- int(idNum)
		}()
	}

	go func() {
		for i := 0; i < n; i++ {
			_, _ = io.WriteString(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`+"\n", i))
		}
	}()

	wg.Wait()
	close(errCh)
	close(got)

	for err := range errCh {
		t.Fatalf("Recv() error: %v", err)
	}

	seen := make(map[int]bool, n)
	for id := range got {
		if seen[id] {
			t.Fatalf("duplicate id received: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("received %d/%d messages", len(seen), n)
	}
}
