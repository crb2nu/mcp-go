package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ProtocolVersion20250618 is the MCP protocol version for Streamable HTTP.
const ProtocolVersion20250618 = "2025-06-18"

// MessageHandler processes an MCP message and returns a response.
// Used by StreamableHTTPServer to bridge HTTP requests to the daemon's message dispatch.
type MessageHandler func(ctx context.Context, msg *Message) (*Message, error)

// StreamableHTTPConfig configures the Streamable HTTP server.
type StreamableHTTPConfig struct {
	// AllowedOrigins restricts which origins can connect (DNS rebinding protection).
	// Empty slice allows all origins.
	AllowedOrigins []string

	// SessionRequired requires Mcp-Session-Id header after initialization.
	SessionRequired bool

	// SessionTimeout is how long idle sessions are kept before expiry.
	SessionTimeout time.Duration

	// MaxSessions limits the number of concurrent sessions (0 = unlimited).
	MaxSessions int
}

// DefaultStreamableHTTPConfig returns sensible defaults.
func DefaultStreamableHTTPConfig() StreamableHTTPConfig {
	return StreamableHTTPConfig{
		SessionRequired: true,
		SessionTimeout:  30 * time.Minute,
		MaxSessions:     1000,
	}
}

// StreamableHTTPServer handles MCP Streamable HTTP transport.
// It implements http.Handler and dispatches JSON-RPC messages to a MessageHandler.
type StreamableHTTPServer struct {
	handler  MessageHandler
	sessions sync.Map // sessionID → *httpSession
	config   StreamableHTTPConfig
	logger   func(msg string, args ...any) // optional structured logger
}

type httpSession struct {
	id          string
	createdAt   time.Time
	lastAccess  time.Time
	initialized bool
	mu          sync.Mutex
}

// NewStreamableHTTPServer creates a new Streamable HTTP server.
func NewStreamableHTTPServer(handler MessageHandler, cfg StreamableHTTPConfig) *StreamableHTTPServer {
	if cfg.SessionTimeout == 0 {
		cfg.SessionTimeout = 30 * time.Minute
	}
	return &StreamableHTTPServer{
		handler: handler,
		config:  cfg,
	}
}

// SetLogger sets an optional structured logger.
func (s *StreamableHTTPServer) SetLogger(fn func(msg string, args ...any)) {
	s.logger = fn
}

func (s *StreamableHTTPServer) log(msg string, args ...any) {
	if s.logger != nil {
		s.logger(msg, args...)
	}
}

// ServeHTTP implements http.Handler.
func (s *StreamableHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handlePOST(w, r)
	case http.MethodGet:
		// Server-initiated messages deferred; return 405.
		http.Error(w, "GET not supported (server-initiated messages not implemented)", http.StatusMethodNotAllowed)
	case http.MethodDelete:
		s.handleDELETE(w, r)
	case http.MethodOptions:
		s.handleOPTIONS(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *StreamableHTTPServer) handlePOST(w http.ResponseWriter, r *http.Request) {
	// Validate Content-Type
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Validate Accept header
	accept := r.Header.Get("Accept")
	if accept == "" {
		accept = "application/json"
	}
	if !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*") {
		http.Error(w, "Accept must include application/json", http.StatusNotAcceptable)
		return
	}

	// Validate Origin (DNS rebinding protection)
	if !s.validateOrigin(r) {
		http.Error(w, "Origin not allowed", http.StatusForbidden)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		http.Error(w, "Empty request body", http.StatusBadRequest)
		return
	}

	// Parse message
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		resp := NewErrorResponse(nil, ParseError, "Invalid JSON: "+err.Error())
		s.writeJSON(w, http.StatusOK, resp)
		return
	}

	// Session validation for non-initialize requests
	sessionID := r.Header.Get("Mcp-Session-Id")

	if msg.Method == "initialize" {
		// Initialize requests must not have a session ID
		resp, err := s.handler(r.Context(), &msg)
		if err != nil {
			s.writeJSON(w, http.StatusOK, NewErrorResponse(msg.ID, InternalError, err.Error()))
			return
		}

		// Create session
		session := &httpSession{
			id:          uuid.New().String(),
			createdAt:   time.Now(),
			lastAccess:  time.Now(),
			initialized: true,
		}

		// Enforce max sessions
		if s.config.MaxSessions > 0 {
			count := 0
			s.sessions.Range(func(_, _ any) bool {
				count++
				return count < s.config.MaxSessions
			})
			if count >= s.config.MaxSessions {
				s.writeJSON(w, http.StatusOK, NewErrorResponse(msg.ID, InternalError, "too many sessions"))
				return
			}
		}

		s.sessions.Store(session.id, session)
		s.log("session created", "session_id", session.id)

		w.Header().Set("Mcp-Session-Id", session.id)
		s.writeJSON(w, http.StatusOK, resp)
		return
	}

	// Non-initialize: validate session if required
	if s.config.SessionRequired {
		if sessionID == "" {
			http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
			return
		}
		val, ok := s.sessions.Load(sessionID)
		if !ok {
			http.Error(w, "Session not found or expired", http.StatusNotFound)
			return
		}
		session := val.(*httpSession)
		session.mu.Lock()
		session.lastAccess = time.Now()
		session.mu.Unlock()
	}

	// Handle notifications (no ID, no response expected)
	if msg.IsNotification() {
		_, _ = s.handler(r.Context(), &msg)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Handle requests
	resp, err := s.handler(r.Context(), &msg)
	if err != nil {
		s.writeJSON(w, http.StatusOK, NewErrorResponse(msg.ID, InternalError, err.Error()))
		return
	}

	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *StreamableHTTPServer) handleDELETE(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}

	if _, ok := s.sessions.LoadAndDelete(sessionID); !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	s.log("session deleted", "session_id", sessionID)
	w.WriteHeader(http.StatusOK)
}

func (s *StreamableHTTPServer) handleOPTIONS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST, GET, DELETE, OPTIONS")
	w.WriteHeader(http.StatusNoContent)
}

func (s *StreamableHTTPServer) validateOrigin(r *http.Request) bool {
	if len(s.config.AllowedOrigins) == 0 {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header (e.g., server-to-server) — allow
		return true
	}
	for _, allowed := range s.config.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (s *StreamableHTTPServer) writeJSON(w http.ResponseWriter, status int, msg *Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}

// ReapExpiredSessions removes sessions that have been idle longer than the timeout.
// Call this periodically from a background goroutine.
func (s *StreamableHTTPServer) ReapExpiredSessions() int {
	reaped := 0
	now := time.Now()
	s.sessions.Range(func(key, value any) bool {
		session := value.(*httpSession)
		session.mu.Lock()
		idle := now.Sub(session.lastAccess)
		session.mu.Unlock()
		if idle > s.config.SessionTimeout {
			s.sessions.Delete(key)
			s.log("session expired", "session_id", session.id, "idle", idle)
			reaped++
		}
		return true
	})
	return reaped
}

// SessionCount returns the number of active sessions.
func (s *StreamableHTTPServer) SessionCount() int {
	count := 0
	s.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// GetSession returns session info for a given session ID.
func (s *StreamableHTTPServer) GetSession(sessionID string) (createdAt, lastAccess time.Time, ok bool) {
	val, found := s.sessions.Load(sessionID)
	if !found {
		return time.Time{}, time.Time{}, false
	}
	session := val.(*httpSession)
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.createdAt, session.lastAccess, true
}

