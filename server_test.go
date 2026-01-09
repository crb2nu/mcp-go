package mcp

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestServer_Concurrency(t *testing.T) {
	// Create a pipe transport
	clientTransport, serverTransport := NewPipeTransport()

	// Create server
	srv := NewServer("test", "1.0.0")
	srv.SetConcurrencyLimit(2) // Allow 2 concurrent requests

	// Add a slow tool
	var slowStarted sync.WaitGroup
	slowStarted.Add(1)
	
	srv.AddTool(Tool{Name: "slow"}, func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		slowStarted.Done()
		time.Sleep(100 * time.Millisecond)
		return TextResult("slow done"), nil
	})

	// Add a fast tool
	srv.AddTool(Tool{Name: "fast"}, func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return TextResult("fast done"), nil
	})

	// Start server in background
	go srv.RunWithTransport(context.Background(), serverTransport)

	// Send slow request
	go func() {
		req, _ := NewRequest(1, "tools/call", CallToolParams{Name: "slow"})
		clientTransport.Send(context.Background(), req)
	}()

	// Wait for slow request to actually start processing
	// We need to ensure the server has picked it up
	slowStarted.Wait()

	// Send fast request immediately
	start := time.Now()
	req, _ := NewRequest(2, "tools/call", CallToolParams{Name: "fast"})
	if err := clientTransport.Send(context.Background(), req); err != nil {
		t.Fatalf("send fast: %v", err)
	}

	// Read responses
	// We expect the fast response to arrive BEFORE the slow response finishes (approx 100ms)
	// Since we are using a pipe, we need to read loop to find the right ID if they arrive out of order (though pipe preserves order of send, the server processing is concurrent)
	
	// Actually, since we want to verify concurrency, we want to receive the fast response quickly.
	// If sequential, fast response would block behind slow response.
	
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := clientTransport.Recv(context.Background())
			if err != nil {
				return
			}
			if msg.ID != nil {
				id, ok := msg.ID.(int64) // Message IDs are parsed as float64 usually if from JSON, but here internal
				// PipeTransport passes struct directly, so types preserved?
				// Let's check type
				if !ok {
					// try int
					if idInt, okInt := msg.ID.(int); okInt {
						id = int64(idInt)
					}
				}
				
				if id == 2 {
					// Fast response received
					if time.Since(start) > 80*time.Millisecond {
						t.Errorf("fast request took too long (%v), likely blocked by slow request", time.Since(start))
					}
					return
				}
			}
		}
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for fast response")
	}
}

func TestServer_Sequential(t *testing.T) {
	// Create a pipe transport
	clientTransport, serverTransport := NewPipeTransport()

	// Create server with limit 1 (default)
	srv := NewServer("test", "1.0.0")
	// srv.SetConcurrencyLimit(1) // Default

	var lock sync.Mutex
	var active int

	// Add tool that asserts no overlap
	srv.AddTool(Tool{Name: "check"}, func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		lock.Lock()
		active++
		if active > 1 {
			t.Errorf("concurrency violation: active=%d", active)
		}
		lock.Unlock()

		time.Sleep(50 * time.Millisecond)

		lock.Lock()
		active--
		lock.Unlock()
		return TextResult("ok"), nil
	})

	go srv.RunWithTransport(context.Background(), serverTransport)

	// Send multiple requests
	for i := 0; i < 5; i++ {
		req, _ := NewRequest(int64(i), "tools/call", CallToolParams{Name: "check"})
		clientTransport.Send(context.Background(), req)
	}

	// Consume responses
	for i := 0; i < 5; i++ {
		_, err := clientTransport.Recv(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}
