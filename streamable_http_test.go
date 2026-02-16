package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func newTestServer(t *testing.T, handler MessageHandler) (*httptest.Server, *StreamableHTTPServer) {
	t.Helper()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(handler, cfg)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("expected Mcp-Session-Id header in response")
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
	initResp.Body.Close()

	// Send notification (no ID)
	notif := &Message{JSONRPC: "2.0", Method: "notifications/initialized"}
	resp := postJSON(t, ts.URL, notif, sessionID)
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
	initResp.Body.Close()

	// DELETE the session
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	req.Header.Set("Mcp-Session-Id", sessionID)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()

	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on DELETE, got %d", delResp.StatusCode)
	}

	// Verify session is gone
	pingMsg, _ := NewRequest(2, "ping", nil)
	resp := postJSON(t, ts.URL, pingMsg, sessionID)
	defer resp.Body.Close()

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
	resp.Body.Close()

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
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for good origin, got %d", resp2.StatusCode)
	}
}

func TestStreamableHTTP_GETReturns405(t *testing.T) {
	mux := http.NewServeMux()
	cfg := DefaultStreamableHTTPConfig()
	srv := NewStreamableHTTPServer(echoHandler, cfg)
	mux.Handle("/mcp", srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

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
	defer transport.Close()

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
	resp.Body.Close()

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
