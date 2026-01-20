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
