package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func echoHandler(ctx context.Context, msg *Message) (*Message, error) {
	if msg.Method == "initialize" {
		return NewResponse(msg.ID, InitializeResult{
			ProtocolVersion: ProtocolVersion20250618,
			Capabilities:    Capabilities{},
			ServerInfo:      ServerInfo{Name: "test", Version: "1.0"},
		})
	}
	if msg.Method == "ping" {
		return NewResponse(msg.ID, map[string]string{"pong": "ok"})
	}
	return NewErrorResponse(msg.ID, MethodNotFound, "unknown method"), nil
}

func postJSON(t *testing.T, url string, msg *Message, sessionID string) *http.Response {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStreamableHTTP_InitializeRoundTrip(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
		Capabilities:    Capabilities{},
		ClientInfo:      ClientInfo{Name: "test-client", Version: "1.0"},
	})

	resp := postJSON(t, ts.URL, initMsg, "")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("expected Mcp-Session-Id header in response")
	}
	if got := resp.Header.Get(protocolVersionHeader); got != ProtocolVersion20250618 {
		t.Fatalf("protocol header = %q, want %q", got, ProtocolVersion20250618)
	}

	var respMsg Message
	if err := json.NewDecoder(resp.Body).Decode(&respMsg); err != nil {
		t.Fatal(err)
	}

	if respMsg.Error != nil {
		t.Fatalf("unexpected error: %s", respMsg.Error.Message)
	}

	var result InitializeResult
	if err := json.Unmarshal(respMsg.Result, &result); err != nil {
		t.Fatal(err)
	}

	if result.ServerInfo.Name != "test" {
		t.Fatalf("expected server name 'test', got %q", result.ServerInfo.Name)
	}
}

func TestStreamableHTTP_ProtocolVersionRequestHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "supported header is honored", header: ProtocolVersion, want: ProtocolVersion},
		{name: "absent header is tolerated", want: ProtocolVersion20250618},
		{name: "unknown header falls back", header: "2026-07-28", want: ProtocolVersion20250618},
	}

	server := NewStreamableHTTPServer(echoHandler, DefaultStreamableHTTPConfig())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set(protocolVersionHeader, tt.header)
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if got := recorder.Header().Get(protocolVersionHeader); got != tt.want {
				t.Fatalf("protocol header = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStreamableHTTP_NotificationReturns202(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// First initialize to get a session
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
	})
	initResp := postJSON(t, ts.URL, initMsg, "")
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	_ = initResp.Body.Close()

	// Send notification (no ID)
	notif := &Message{JSONRPC: "2.0", Method: "notifications/initialized"}
	resp := postJSON(t, ts.URL, notif, sessionID)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_SessionRequired(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.SessionRequired = true
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Send a non-initialize request without session ID
	pingMsg, _ := NewRequest(2, "ping", nil)
	resp := postJSON(t, ts.URL, pingMsg, "")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without session, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_InvalidSession(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	pingMsg, _ := NewRequest(2, "ping", nil)
	resp := postJSON(t, ts.URL, pingMsg, "nonexistent-session")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for invalid session, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_SessionDelete(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Initialize to get session
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
	})
	initResp := postJSON(t, ts.URL, initMsg, "")
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	_ = initResp.Body.Close()

	// DELETE the session
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	req.Header.Set("Mcp-Session-Id", sessionID)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = delResp.Body.Close()

	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on DELETE, got %d", delResp.StatusCode)
	}

	// Verify session is gone
	pingMsg, _ := NewRequest(2, "ping", nil)
	resp := postJSON(t, ts.URL, pingMsg, sessionID)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after session delete, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_OriginValidation(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.AllowedOrigins = []string{"https://good.example.com"}
	cfg.SessionRequired = false
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	msg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
	})
	body, _ := json.Marshal(msg)

	// Bad origin
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for bad origin, got %d", resp.StatusCode)
	}

	// Good origin
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(string(body)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json")
	req2.Header.Set("Origin", "https://good.example.com")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for good origin, got %d", resp2.StatusCode)
	}
}

func TestStreamableHTTP_GETReturns405WhenDisabled(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	// SSEEnabled defaults to false
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_ClientTransportRoundTrip(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPClientConfig{
		Endpoint: ts.URL + "/mcp",
	})
	defer func() { _ = transport.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
		Capabilities:    Capabilities{},
		ClientInfo:      ClientInfo{Name: "test-transport", Version: "1.0"},
	})

	if err := transport.Send(ctx, initMsg); err != nil {
		t.Fatal(err)
	}

	resp, err := transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	// Session ID should be captured
	if transport.SessionID() == "" {
		t.Fatal("expected session ID to be set after initialize")
	}

	// Send initialized notification
	notif := &Message{JSONRPC: "2.0", Method: "notifications/initialized"}
	if err := transport.Send(ctx, notif); err != nil {
		t.Fatal(err)
	}

	// Send ping request
	pingMsg, _ := NewRequest(2, "ping", nil)
	if err := transport.Send(ctx, pingMsg); err != nil {
		t.Fatal(err)
	}

	pingResp, err := transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if pingResp.Error != nil {
		t.Fatalf("unexpected error on ping: %s", pingResp.Error.Message)
	}
}

func TestStreamableHTTP_OversizedRequestBody(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.MaxRequestBodySize = 64 // tiny limit for testing
	cfg.SessionRequired = false
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Build a body that exceeds the 64-byte limit.
	oversized := strings.Repeat("x", 128)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Request Entity Too Large, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_RequestBodyWithinLimit(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.MaxRequestBodySize = 4096 // generous limit
	cfg.SessionRequired = false
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// A normal-sized initialize request should succeed.
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
		Capabilities:    Capabilities{},
		ClientInfo:      ClientInfo{Name: "test-client", Version: "1.0"},
	})

	resp := postJSON(t, ts.URL, initMsg, "")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for request within limit, got %d", resp.StatusCode)
	}
}

func TestStreamableHTTP_ReapExpiredSessions(t *testing.T) {
	cfg := DefaultStreamableHTTPConfig()
	cfg.SessionTimeout = 10 * time.Millisecond
	srv := NewStreamableHTTPServer(echoHandler, cfg)

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Create a session
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
	})
	resp := postJSON(t, ts.URL, initMsg, "")
	_ = resp.Body.Close()

	if srv.SessionCount() != 1 {
		t.Fatalf("expected 1 session, got %d", srv.SessionCount())
	}

	// Wait for session to expire
	time.Sleep(20 * time.Millisecond)

	reaped := srv.ReapExpiredSessions()
	if reaped != 1 {
		t.Fatalf("expected 1 reaped session, got %d", reaped)
	}

	if srv.SessionCount() != 0 {
		t.Fatalf("expected 0 sessions after reap, got %d", srv.SessionCount())
	}
}

// --- SSE GET endpoint tests ---

// newSSETestServer creates a test server with SSE enabled and returns
// the httptest.Server, the StreamableHTTPServer, and an initialized session ID.
func newSSETestServer(t *testing.T) (*httptest.Server, *StreamableHTTPServer, string) {
	t.Helper()
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.SSEEnabled = true
	cfg.SSEKeepaliveInterval = 100 * time.Millisecond // fast for tests
	cfg.NotificationBufferSize = 16
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Initialize to get a session ID.
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
	})
	resp := postJSON(t, ts.URL, initMsg, "")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	_ = resp.Body.Close()
	if sessionID == "" {
		t.Fatal("expected session ID from initialize")
	}
	return ts, srv, sessionID
}

// doSSEGet opens an SSE GET connection and returns the response.
// The caller must close resp.Body.
func doSSEGet(t *testing.T, url, sessionID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// readSSEEvent reads one SSE event from an SSE stream.
// Returns the event type and data fields. Skips comment lines.
func readSSEEvent(t *testing.T, scanner *bufio.Scanner) (eventType, data string) {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// End of event block.
			if eventType != "" || data != "" {
				return eventType, data
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment (e.g., keepalive). Return it as a special event type.
			return "comment", strings.TrimPrefix(line, ": ")
		}
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	return "", ""
}

func TestStreamableHTTP_GETReturnsSSEStream(t *testing.T) {
	ts, _, sessionID := newSSETestServer(t)

	resp := doSSEGet(t, ts.URL, sessionID)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	cc := resp.Header.Get("Cache-Control")
	if cc != "no-cache" {
		t.Fatalf("expected Cache-Control no-cache, got %q", cc)
	}
}

func TestStreamableHTTP_GETRequiresSession(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.SSEEnabled = true
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Missing session header.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing session, got %d", resp.StatusCode)
	}

	// Invalid session ID.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/mcp", nil)
	req2.Header.Set("Accept", "text/event-stream")
	req2.Header.Set("Mcp-Session-Id", "nonexistent-session")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for invalid session, got %d", resp2.StatusCode)
	}
}

func TestStreamableHTTP_SendNotification(t *testing.T) {
	ts, srv, sessionID := newSSETestServer(t)

	// Open SSE stream in a goroutine and read events.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Give the SSE stream a moment to establish.
	time.Sleep(20 * time.Millisecond)

	// Send a notification.
	notifMsg := &Message{
		JSONRPC: JSONRPCVersion,
		Method:  "notifications/test",
	}
	if err := srv.SendNotification(sessionID, notifMsg); err != nil {
		t.Fatalf("SendNotification failed: %v", err)
	}

	// Read the SSE event.
	scanner := bufio.NewScanner(resp.Body)
	eventType, data := readSSEEvent(t, scanner)
	if eventType != "message" {
		t.Fatalf("expected event type 'message', got %q", eventType)
	}

	var received Message
	if err := json.Unmarshal([]byte(data), &received); err != nil {
		t.Fatalf("failed to unmarshal SSE data: %v", err)
	}
	if received.Method != "notifications/test" {
		t.Fatalf("expected method 'notifications/test', got %q", received.Method)
	}
}

func TestStreamableHTTP_BroadcastNotification(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.SSEEnabled = true
	cfg.SSEKeepaliveInterval = 5 * time.Second // long enough to not interfere
	cfg.NotificationBufferSize = 16
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Create two sessions.
	var sessionIDs [2]string
	for i := range sessionIDs {
		initMsg, _ := NewRequest(i+1, "initialize", InitializeParams{
			ProtocolVersion: ProtocolVersion20250618,
		})
		resp := postJSON(t, ts.URL, initMsg, "")
		sessionIDs[i] = resp.Header.Get("Mcp-Session-Id")
		_ = resp.Body.Close()
		if sessionIDs[i] == "" {
			t.Fatalf("session %d: expected session ID", i)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Open SSE streams for both sessions.
	var responses [2]*http.Response
	for i, sid := range sessionIDs {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Mcp-Session-Id", sid)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			t.Fatalf("session %d: expected 200, got %d", i, resp.StatusCode)
		}
		responses[i] = resp
		defer func() { _ = resp.Body.Close() }()
	}

	// Give SSE streams time to establish.
	time.Sleep(20 * time.Millisecond)

	// Broadcast a notification.
	broadcastMsg := &Message{
		JSONRPC: JSONRPCVersion,
		Method:  "notifications/broadcast",
	}
	srv.BroadcastNotification(broadcastMsg)

	// Verify both sessions receive the notification.
	var wg sync.WaitGroup
	for i, resp := range responses {
		wg.Add(1)
		go func(idx int, r *http.Response) {
			defer wg.Done()
			scanner := bufio.NewScanner(r.Body)
			eventType, data := readSSEEvent(t, scanner)
			if eventType != "message" {
				t.Errorf("session %d: expected event type 'message', got %q", idx, eventType)
				return
			}
			var received Message
			if err := json.Unmarshal([]byte(data), &received); err != nil {
				t.Errorf("session %d: failed to unmarshal: %v", idx, err)
				return
			}
			if received.Method != "notifications/broadcast" {
				t.Errorf("session %d: expected method 'notifications/broadcast', got %q", idx, received.Method)
			}
		}(i, resp)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for broadcast notifications")
	}
}

func TestStreamableHTTP_SSEKeepalive(t *testing.T) {
	ts, _, sessionID := newSSETestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The SSEKeepaliveInterval is 100ms in the test server.
	// Read an event; it should be a keepalive comment within ~200ms.
	scanner := bufio.NewScanner(resp.Body)
	eventType, data := readSSEEvent(t, scanner)
	if eventType != "comment" {
		t.Fatalf("expected comment (keepalive), got event type %q data %q", eventType, data)
	}
	if data != "keepalive" {
		t.Fatalf("expected keepalive comment, got %q", data)
	}
}

func TestStreamableHTTP_SSEClosedOnSessionDelete(t *testing.T) {
	ts, _, sessionID := newSSETestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Give the SSE stream time to establish.
	time.Sleep(20 * time.Millisecond)

	// DELETE the session.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	delReq.Header.Set("Mcp-Session-Id", sessionID)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on DELETE, got %d", delResp.StatusCode)
	}

	// The SSE stream's notifCh was closed; reading should terminate.
	// Try to read — the scanner should finish without producing more events
	// or the body should reach EOF quickly.
	scanner := bufio.NewScanner(resp.Body)
	done := make(chan struct{})
	go func() {
		// Drain any remaining data.
		for scanner.Scan() {
			// consume
		}
		close(done)
	}()

	select {
	case <-done:
		// Stream closed as expected.
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream did not close after session delete")
	}
}

func TestStreamableHTTP_SingleSSEPerSession(t *testing.T) {
	ts, _, sessionID := newSSETestServer(t)

	// Open first SSE stream.
	resp1 := doSSEGet(t, ts.URL, sessionID)
	defer func() { _ = resp1.Body.Close() }()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first GET: expected 200, got %d", resp1.StatusCode)
	}

	// Give first stream time to establish.
	time.Sleep(20 * time.Millisecond)

	// Second GET should return 409.
	resp2 := doSSEGet(t, ts.URL, sessionID)
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second GET: expected 409, got %d", resp2.StatusCode)
	}
}

func TestStreamableHTTP_SendNotification_SessionNotFound(t *testing.T) {
	cfg := DefaultStreamableHTTPConfig()
	cfg.SSEEnabled = true
	srv := NewStreamableHTTPServer(echoHandler, cfg)

	err := srv.SendNotification("nonexistent", &Message{JSONRPC: JSONRPCVersion, Method: "test"})
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got: %v", err)
	}
}

func TestStreamableHTTP_SendNotification_BufferFull(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	cfg.SSEEnabled = true
	cfg.NotificationBufferSize = 2 // tiny buffer
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Initialize to get a session.
	initMsg, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion20250618,
	})
	resp := postJSON(t, ts.URL, initMsg, "")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	_ = resp.Body.Close()

	// Fill the buffer without connecting an SSE client.
	msg := &Message{JSONRPC: JSONRPCVersion, Method: "test"}
	for i := 0; i < 2; i++ {
		if err := srv.SendNotification(sessionID, msg); err != nil {
			t.Fatalf("send %d: unexpected error: %v", i, err)
		}
	}

	// Third should fail (buffer full).
	err := srv.SendNotification(sessionID, msg)
	if err == nil {
		t.Fatal("expected error for full buffer")
	}
	if !strings.Contains(err.Error(), "buffer full") {
		t.Fatalf("expected 'buffer full' in error, got: %v", err)
	}
}
