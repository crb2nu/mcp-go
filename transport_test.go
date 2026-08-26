package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerDiscoverOverStdioFraming(t *testing.T) {
	request := `{"jsonrpc":"2.0","id":"probe-7","method":"server/discover","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`
	for _, contentLength := range []bool{false, true} {
		t.Run(fmt.Sprintf("content_length_%t", contentLength), func(t *testing.T) {
			input := request + "\n"
			if contentLength {
				input = fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(request), request)
			}
			var output bytes.Buffer
			srv := NewServer("stdio", "1.0")
			if err := srv.RunWithIO(context.Background(), strings.NewReader(input), &output); err != nil {
				t.Fatal(err)
			}

			payload := output.Bytes()
			if contentLength {
				reader := bufio.NewReader(bytes.NewReader(payload))
				header, err := reader.ReadString('\n')
				if err != nil || !strings.HasPrefix(header, "Content-Length: ") {
					t.Fatalf("invalid response framing: %q err=%v", payload, err)
				}
				length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "Content-Length: ")))
				if err != nil {
					t.Fatal(err)
				}
				_, _ = reader.ReadString('\n')
				payload = make([]byte, length)
				if _, err := io.ReadFull(reader, payload); err != nil {
					t.Fatal(err)
				}
			} else if !bytes.HasSuffix(payload, []byte("\n")) {
				t.Fatalf("newline response missing delimiter: %q", payload)
			}

			var response Message
			if err := json.Unmarshal(bytes.TrimSpace(payload), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != "probe-7" || response.Error != nil {
				t.Fatalf("unexpected response: %+v", response)
			}
		})
	}
}

func TestStdioTransportRecv_IgnoresNonJSONLines(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, io.Discard)
	t.Cleanup(func() { _ = tr.Close() })

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
	t.Cleanup(func() { _ = shared.Close() })

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

func TestStdioTransportRecv_ContextCancel(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, io.Discard)
	t.Cleanup(func() { _ = tr.Close() })

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := tr.Recv(ctx)
		done <- err
	}()

	// Let the goroutine start its blocking read.
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after context cancellation")
	}
}

// TestStdioTransportRecv_ReusableAfterCancel verifies that the transport
// remains functional after a context cancellation. This is the key behavioral
// change from DEBT-009: context cancel no longer destroys the transport.
func TestStdioTransportRecv_ReusableAfterCancel(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	var sendBuf bytes.Buffer
	tr := NewStdioTransport(r, &sendBuf)
	t.Cleanup(func() { _ = tr.Close() })

	// Recv with short timeout — no data available, should return ctx error.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	_, err := tr.Recv(ctx1)
	if err != context.DeadlineExceeded {
		t.Fatalf("first Recv: expected DeadlineExceeded, got: %v", err)
	}

	// Write a message — transport should still be alive.
	go func() {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	}()

	// Second Recv should succeed.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	msg, err := tr.Recv(ctx2)
	if err != nil {
		t.Fatalf("second Recv: unexpected error: %v", err)
	}
	if msg.Method != "ping" {
		t.Fatalf("msg.Method = %q, want %q", msg.Method, "ping")
	}

	// Send should also still work.
	resp := &Message{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: []byte(`{"ok":true}`)}
	if err := tr.Send(context.Background(), resp); err != nil {
		t.Fatalf("Send after cancel: %v", err)
	}
}

func TestStdioTransportClose_ClosesUnderlying(t *testing.T) {
	r, w := io.Pipe()

	tr := NewStdioTransport(r, w)

	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	// Reader should be closed.
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err == nil {
		t.Fatal("expected error reading from closed pipe reader")
	}

	// Writer should be closed.
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("expected error writing to closed pipe writer")
	}

	// Close is idempotent.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
}

func TestStdioTransportRecv_AfterClose(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, io.Discard)
	_ = tr.Close()

	ctx := context.Background()
	_, err := tr.Recv(ctx)
	if err == nil {
		t.Fatal("expected error from Recv after Close")
	}
	if err.Error() != "transport closed" {
		t.Fatalf("expected 'transport closed', got: %v", err)
	}
}

func TestStdioTransportSend_AfterClose(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, w)
	_ = tr.Close()

	msg := &Message{JSONRPC: JSONRPCVersion, Method: "ping"}
	err := tr.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from Send after Close")
	}
	if err.Error() != "transport closed" {
		t.Fatalf("expected 'transport closed', got: %v", err)
	}
}

func TestStdioTransportRecv_Concurrent(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })

	tr := NewStdioTransport(r, io.Discard)
	t.Cleanup(func() { _ = tr.Close() })

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
