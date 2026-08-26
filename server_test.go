package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServer_DiscoverAndHandshakeLessToolCall(t *testing.T) {
	srv := NewServer("discoverable", "1.2.3")
	srv.SetInstructions("test instructions")
	var effectiveVersion string
	srv.AddTool(Tool{Name: "echo", InputSchema: InputSchema{Type: "object"}}, func(ctx context.Context, _ map[string]any) (*CallToolResult, error) {
		effectiveVersion, _ = ProtocolVersionFromContext(ctx)
		return TextResult("ok"), nil
	})

	discover, _ := NewRequest("discover-id", "server/discover", DiscoverParams{Meta: &RequestMeta{ProtocolVersion: ProtocolVersion20260728}})
	resp, err := srv.handleMessage(context.Background(), discover)
	if err != nil || resp.Error != nil {
		t.Fatalf("discover failed: resp=%+v err=%v", resp, err)
	}
	var result DiscoverResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if resp.ID != "discover-id" || result.ServerInfo.Name != "discoverable" || result.ServerInfo.Version != "1.2.3" {
		t.Fatalf("unexpected discovery identity: id=%v server=%+v", resp.ID, result.ServerInfo)
	}
	if len(result.SupportedProtocolVersions) != 3 || result.SupportedProtocolVersions[2] != ProtocolVersion20260728 {
		t.Fatalf("supported versions = %v", result.SupportedProtocolVersions)
	}
	if result.Capabilities.Tools == nil || len(result.Tools) != 1 || result.Tools[0].Name != "echo" {
		t.Fatalf("unexpected discovery tools: capabilities=%+v tools=%+v", result.Capabilities, result.Tools)
	}

	call, _ := NewRequest(2, "tools/call", CallToolParamsWithMeta{
		CallToolParams: CallToolParams{Name: "echo"},
		Meta:           &CallToolMeta{ProtocolVersion: ProtocolVersion20260728},
	})
	resp, err = srv.handleMessage(context.Background(), call)
	if err != nil || resp.Error != nil {
		t.Fatalf("handshake-less call failed: resp=%+v err=%v", resp, err)
	}
	var callResult CallToolResult
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		t.Fatal(err)
	}
	if effectiveVersion != ProtocolVersion20260728 || callResult.ResultType != ResultTypeComplete {
		t.Fatalf("version=%q resultType=%q", effectiveVersion, callResult.ResultType)
	}
}

func TestServer_UnsupportedPerRequestProtocolVersion(t *testing.T) {
	srv := NewServer("test", "1")
	req, _ := NewRequest(1, "tools/list", RequestParams{Meta: &RequestMeta{ProtocolVersion: "2099-01-01"}})
	resp, err := srv.handleMessage(context.Background(), req)
	if err != nil || resp.Error == nil || resp.Error.Code != InvalidParams {
		t.Fatalf("expected InvalidParams response, got resp=%+v err=%v", resp, err)
	}
}

func TestServer_ResultTypeCompleteOnListsAndToolErrors(t *testing.T) {
	srv := NewServer("test", "1")
	srv.AddTool(Tool{Name: "fail"}, func(context.Context, map[string]any) (*CallToolResult, error) {
		return nil, fmt.Errorf("boom")
	})

	for _, method := range []string{"tools/list", "resources/list", "prompts/list"} {
		req, _ := NewRequest(method, method, map[string]any{})
		resp, err := srv.handleMessage(context.Background(), req)
		if err != nil || resp.Error != nil {
			t.Fatalf("%s failed: resp=%+v err=%v", method, resp, err)
		}
		var result struct {
			ResultType string `json:"resultType"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.ResultType != ResultTypeComplete {
			t.Fatalf("%s resultType=%q", method, result.ResultType)
		}
	}

	req, _ := NewRequest(1, "tools/call", CallToolParams{Name: "fail"})
	resp, err := srv.handleMessage(context.Background(), req)
	if err != nil || resp.Error != nil {
		t.Fatalf("tool error result failed: resp=%+v err=%v", resp, err)
	}
	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ResultType != ResultTypeComplete || !result.IsError {
		t.Fatalf("unexpected tool error result: %+v", result)
	}
}

func TestServer_ListResultsAreDeterministic(t *testing.T) {
	orders := [][]string{{"zeta", "alpha", "middle"}, {"middle", "zeta", "alpha"}}
	results := make([]map[string]json.RawMessage, len(orders))

	for i, order := range orders {
		srv := NewServer("test", "1")
		for _, name := range order {
			srv.AddTool(Tool{Name: name}, nil)
			srv.AddResource(Resource{Name: name, URI: "resource://" + name}, nil)
			srv.AddResourceTemplate(ResourceTemplate{Name: name, URITemplate: "template://" + name + "/{id}"}, nil)
			srv.AddPrompt(Prompt{Name: name}, nil)
		}

		results[i] = make(map[string]json.RawMessage)
		for _, method := range []string{"tools/list", "resources/list", "prompts/list"} {
			req, _ := NewRequest(1, method, map[string]any{})
			resp, err := srv.handleMessage(context.Background(), req)
			if err != nil || resp.Error != nil {
				t.Fatalf("%s failed: resp=%+v err=%v", method, resp, err)
			}
			results[i][method] = append(json.RawMessage(nil), resp.Result...)
		}

		if got := []string{srv.Tools()[0].Name, srv.Tools()[1].Name, srv.Tools()[2].Name}; !equalStrings(got, order) {
			t.Fatalf("registration order mutated: got %v, want %v", got, order)
		}
	}

	for _, method := range []string{"tools/list", "resources/list", "prompts/list"} {
		if string(results[0][method]) != string(results[1][method]) {
			t.Errorf("%s differs by registration order:\n%s\n%s", method, results[0][method], results[1][method])
		}
	}
}

func TestServer_ListCacheMetadata(t *testing.T) {
	for _, tt := range []struct {
		name       string
		server     *Server
		wantFields bool
	}{
		{name: "omitted by default", server: NewServer("test", "1")},
		{name: "configured", server: NewServer("test", "1", WithListCache(60000, "server")), wantFields: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantFields {
				tt.server.AddTool(Tool{Name: "zeta"}, nil)
				tt.server.AddTool(Tool{Name: "alpha"}, nil)
			}
			for _, method := range []string{"tools/list", "resources/list", "prompts/list"} {
				req, _ := NewRequest(1, method, map[string]any{})
				resp, err := tt.server.handleMessage(context.Background(), req)
				if err != nil || resp.Error != nil {
					t.Fatalf("%s failed: resp=%+v err=%v", method, resp, err)
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(resp.Result, &raw); err != nil {
					t.Fatal(err)
				}
				_, hasTTL := raw["ttlMs"]
				_, hasScope := raw["cacheScope"]
				if hasTTL != tt.wantFields || hasScope != tt.wantFields {
					t.Errorf("%s cache fields: ttlMs=%v cacheScope=%v, want both %v; JSON=%s", method, hasTTL, hasScope, tt.wantFields, resp.Result)
				}
				if tt.wantFields && (string(raw["ttlMs"]) != "60000" || string(raw["cacheScope"]) != `"server"`) {
					t.Errorf("%s cache values incorrect: %s", method, resp.Result)
				}
				if tt.wantFields && method == "tools/list" {
					t.Logf("configured tools/list JSON: %s", resp.Result)
				}
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestServer_ResultTypeCompleteOnNilToolResults(t *testing.T) {
	tests := []struct {
		name     string
		addTool  func(*Server)
		toolName string
	}{
		{
			name: "regular",
			addTool: func(srv *Server) {
				srv.AddTool(Tool{Name: "nil"}, func(context.Context, map[string]any) (*CallToolResult, error) {
					return nil, nil
				})
			},
			toolName: "nil",
		},
		{
			name: "streaming",
			addTool: func(srv *Server) {
				srv.AddStreamingTool(Tool{Name: "nil-stream"}, func(context.Context, map[string]any, chan<- StreamUpdate) (*CallToolResult, error) {
					return nil, nil
				})
			},
			toolName: "nil-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer("test", "1")
			tt.addTool(srv)
			req, _ := NewRequest(1, "tools/call", CallToolParams{Name: tt.toolName})
			resp, err := srv.handleMessage(context.Background(), req)
			if err != nil || resp.Error != nil {
				t.Fatalf("tool call failed: resp=%+v err=%v", resp, err)
			}
			var result CallToolResult
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.ResultType != ResultTypeComplete {
				t.Fatalf("resultType=%q, want %q", result.ResultType, ResultTypeComplete)
			}
		})
	}
}

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
	go func() { _ = srv.RunWithTransport(context.Background(), serverTransport) }()

	// Send slow request
	go func() {
		req, _ := NewRequest(1, "tools/call", CallToolParams{Name: "slow"})
		_ = clientTransport.Send(context.Background(), req)
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

	go func() { _ = srv.RunWithTransport(context.Background(), serverTransport) }()

	// Send multiple requests
	for i := 0; i < 5; i++ {
		req, _ := NewRequest(int64(i), "tools/call", CallToolParams{Name: "check"})
		_ = clientTransport.Send(context.Background(), req)
	}

	// Consume responses
	for i := 0; i < 5; i++ {
		_, err := clientTransport.Recv(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestServer_Resources(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()

	srv := NewServer("test", "1.0.0")

	// Add a static resource
	srv.AddResource(Resource{
		URI:         "file:///readme.md",
		Name:        "README",
		Description: "Project readme",
		MimeType:    "text/markdown",
	}, func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return MarkdownResource(uri, "# Hello World"), nil
	})

	// Add a resource template
	srv.AddResourceTemplate(ResourceTemplate{
		URITemplate: "file:///{path}",
		Name:        "File",
		Description: "Access files by path",
		MimeType:    "text/plain",
	}, func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		// Extract path from URI
		path := strings.TrimPrefix(uri, "file:///")
		return TextResource(uri, fmt.Sprintf("Contents of %s", path)), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Test resources/list
	req, _ := NewRequest(1, "resources/list", nil)
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	var listResult ResourcesListResultWithTemplates
	if err := json.Unmarshal(resp.Result, &listResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(listResult.Resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(listResult.Resources))
	}
	if listResult.Resources[0].URI != "file:///readme.md" {
		t.Errorf("unexpected resource URI: %s", listResult.Resources[0].URI)
	}

	if len(listResult.ResourceTemplates) != 1 {
		t.Errorf("expected 1 template, got %d", len(listResult.ResourceTemplates))
	}

	// Test resources/read for static resource
	req, _ = NewRequest(2, "resources/read", ReadResourceParams{URI: "file:///readme.md"})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err = clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	var readResult ReadResourceResult
	if err := json.Unmarshal(resp.Result, &readResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(readResult.Contents) != 1 {
		t.Errorf("expected 1 content, got %d", len(readResult.Contents))
	}
	if readResult.Contents[0].Text != "# Hello World" {
		t.Errorf("unexpected content: %s", readResult.Contents[0].Text)
	}

	// Test resources/read for template resource
	req, _ = NewRequest(3, "resources/read", ReadResourceParams{URI: "file:///src/main.go"})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err = clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	if err := json.Unmarshal(resp.Result, &readResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !strings.Contains(readResult.Contents[0].Text, "src/main.go") {
		t.Errorf("template handler not invoked correctly: %s", readResult.Contents[0].Text)
	}
}

func TestServer_Prompts(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()

	srv := NewServer("test", "1.0.0")

	// Add a prompt with arguments
	srv.AddPrompt(Prompt{
		Name:        "greet",
		Description: "Generate a greeting",
		Arguments: []PromptArgument{
			{Name: "name", Description: "Name to greet", Required: true},
			{Name: "style", Description: "Greeting style", Required: false},
		},
	}, func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		name := args["name"]
		style := args["style"]
		if style == "" {
			style = "friendly"
		}

		return &GetPromptResult{
			Description: "A personalized greeting",
			Messages: []PromptMessage{
				UserMessage(fmt.Sprintf("Greet %s in a %s way", name, style)),
			},
		}, nil
	})

	// Add a multi-turn prompt
	srv.AddPrompt(Prompt{
		Name:        "code-review",
		Description: "Code review workflow",
	}, func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		return MultiMessagePrompt(
			UserMessage("Please review the following code for issues:"),
			AssistantMessage("I'll analyze the code for potential issues, including bugs, security vulnerabilities, and style problems."),
		), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Test prompts/list
	req, _ := NewRequest(1, "prompts/list", nil)
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	var listResult PromptsListResult
	if err := json.Unmarshal(resp.Result, &listResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(listResult.Prompts) != 2 {
		t.Errorf("expected 2 prompts, got %d", len(listResult.Prompts))
	}

	// Test prompts/get with arguments
	req, _ = NewRequest(2, "prompts/get", GetPromptParams{
		Name:      "greet",
		Arguments: map[string]string{"name": "Alice", "style": "formal"},
	})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err = clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	var getResult GetPromptResult
	if err := json.Unmarshal(resp.Result, &getResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(getResult.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(getResult.Messages))
	}
	if !strings.Contains(getResult.Messages[0].Content.Text, "Alice") {
		t.Errorf("argument not interpolated: %s", getResult.Messages[0].Content.Text)
	}
	if !strings.Contains(getResult.Messages[0].Content.Text, "formal") {
		t.Errorf("style not applied: %s", getResult.Messages[0].Content.Text)
	}

	// Test prompts/get for multi-turn
	req, _ = NewRequest(3, "prompts/get", GetPromptParams{Name: "code-review"})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err = clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	if err := json.Unmarshal(resp.Result, &getResult); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(getResult.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(getResult.Messages))
	}
	if getResult.Messages[0].Role != "user" {
		t.Errorf("expected user role, got %s", getResult.Messages[0].Role)
	}
	if getResult.Messages[1].Role != "assistant" {
		t.Errorf("expected assistant role, got %s", getResult.Messages[1].Role)
	}
}

func TestServer_Initialize_Capabilities(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()

	srv := NewServer("test", "1.0.0")
	srv.EnableSampling()

	// Add a tool, resource, and prompt
	srv.AddTool(Tool{Name: "test"}, func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return TextResult("ok"), nil
	})
	srv.AddResource(Resource{URI: "test://res"}, func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return TextResource(uri, "data"), nil
	})
	srv.AddPrompt(Prompt{Name: "test"}, func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		return TextPrompt("hello"), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Send initialize
	req, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      ClientInfo{Name: "test-client", Version: "1.0.0"},
	})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Check all capabilities are advertised
	if result.Capabilities.Tools == nil {
		t.Error("expected tools capability")
	}
	if result.Capabilities.Resources == nil {
		t.Error("expected resources capability")
	}
	if result.Capabilities.Resources != nil && !result.Capabilities.Resources.Subscribe {
		t.Error("expected resources subscribe capability")
	}
	if result.Capabilities.Resources != nil && !result.Capabilities.Resources.ListChanged {
		t.Error("expected resources listChanged capability")
	}
	if result.Capabilities.Prompts == nil {
		t.Error("expected prompts capability")
	}
	if result.Capabilities.Sampling == nil {
		t.Error("expected sampling capability")
	}
}

func TestServer_ToolsListIncludesOutputSchema(t *testing.T) {
	srv := NewServer("test", "1.0.0")
	srv.AddTool(Tool{
		Name:        "structured",
		InputSchema: InputSchema{Type: "object"},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"files": map[string]any{"type": "array"}},
		},
	}, func(context.Context, map[string]any) (*CallToolResult, error) { return TextResult("ok"), nil })

	req, err := NewRequest(1, "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.handleMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	var result ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := result.Tools[0].OutputSchema["type"]; got != "object" {
		t.Fatalf("output schema type = %#v, want object", got)
	}
}

func TestCallToolResultStructuredContentJSONTags(t *testing.T) {
	legacy, err := json.Marshal(CallToolResult{Content: []Content{{Type: "text", Text: "ok"}}})
	if err != nil {
		t.Fatalf("marshal legacy result: %v", err)
	}
	if strings.Contains(string(legacy), "structuredContent") {
		t.Fatalf("nil structured content was not omitted: %s", legacy)
	}

	structured, err := json.Marshal(CallToolResult{
		Content:           []Content{{Type: "text", Text: "fallback"}},
		StructuredContent: map[string]any{"files": []string{"a", "b", "c"}},
	})
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	if !strings.Contains(string(structured), `"structuredContent":{"files":["a","b","c"]}`) {
		t.Fatalf("incorrect structuredContent JSON: %s", structured)
	}
}

func TestServer_Initialize_ProtocolVersionNegotiation(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{name: "legacy is echoed", requested: ProtocolVersion, want: ProtocolVersion},
		{name: "latest is echoed", requested: ProtocolVersion20250618, want: ProtocolVersion20250618},
		{name: "unknown newer falls back", requested: "2026-07-28", want: ProtocolVersion20250618},
		{name: "absent falls back", want: ProtocolVersion20250618},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer("test", "1.0.0")
			req, err := NewRequest(1, "initialize", InitializeParams{ProtocolVersion: tt.requested})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.handleInitialize(req)
			if err != nil {
				t.Fatal(err)
			}
			var result InitializeResult
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.ProtocolVersion != tt.want {
				t.Fatalf("protocol version = %q, want %q", result.ProtocolVersion, tt.want)
			}
		})
	}
}

func TestServer_Resources_UnknownURI(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()

	srv := NewServer("test", "1.0.0")
	srv.AddResource(Resource{URI: "test://known"}, func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return TextResource(uri, "data"), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Request unknown resource
	req, _ := NewRequest(1, "resources/read", ReadResourceParams{URI: "test://unknown"})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	if resp.Error == nil {
		t.Error("expected error for unknown resource")
	}
	if !strings.Contains(resp.Error.Message, "unknown resource") {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
}

func TestServer_Prompts_UnknownPrompt(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()

	srv := NewServer("test", "1.0.0")
	srv.AddPrompt(Prompt{Name: "known"}, func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		return TextPrompt("hello"), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Request unknown prompt
	req, _ := NewRequest(1, "prompts/get", GetPromptParams{Name: "unknown"})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	resp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	if resp.Error == nil {
		t.Error("expected error for unknown prompt")
	}
	if !strings.Contains(resp.Error.Message, "unknown prompt") {
		t.Errorf("unexpected error: %s", resp.Error.Message)
	}
}

func TestServer_StreamingTool(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()

	srv := NewServer("test", "1.0.0")

	// Add a streaming tool that sends progress updates
	srv.AddStreamingTool(Tool{
		Name:        "long-process",
		Description: "A process that sends progress updates",
	}, func(ctx context.Context, args map[string]any, updates chan<- StreamUpdate) (*CallToolResult, error) {
		// Simulate a multi-step process
		steps := 5
		for i := 1; i <= steps; i++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				updates <- StreamUpdate{
					Progress: float64(i) / float64(steps),
					Message:  fmt.Sprintf("Step %d of %d", i, steps),
				}
				time.Sleep(10 * time.Millisecond)
			}
		}

		return TextResult("Process complete"), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Call the streaming tool
	start := time.Now()
	req, _ := NewRequest(1, "tools/call", CallToolParamsWithMeta{
		CallToolParams: CallToolParams{Name: "long-process"},
		Meta:           &CallToolMeta{ProgressToken: "progress-123"},
	})
	if err := clientTransport.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Collect all messages (progress notifications and final response)
	var responses []*Message
	timeout := time.After(2 * time.Second)
	done := false

	for !done {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for response")
		default:
			msg, err := clientTransport.Recv(ctx)
			if err != nil {
				t.Fatalf("recv: %v", err)
			}
			responses = append(responses, msg)
			// Check if this is the final response (has an ID matching our request)
			if msg.ID != nil {
				done = true
			}
		}
	}

	duration := time.Since(start)

	// Should complete in roughly 50ms (5 steps * 10ms)
	if duration < 40*time.Millisecond || duration > 200*time.Millisecond {
		t.Logf("streaming tool took %v (expected ~50ms)", duration)
	}

	// The final response should be success
	finalResp := responses[len(responses)-1]
	if finalResp.Error != nil {
		t.Errorf("expected success, got error: %s", finalResp.Error.Message)
	}

	var result CallToolResult
	if err := json.Unmarshal(finalResp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(result.Content) == 0 || result.Content[0].Text != "Process complete" {
		t.Errorf("unexpected result: %+v", result)
	}
	if result.ResultType != ResultTypeComplete {
		t.Errorf("resultType = %q, want %q", result.ResultType, ResultTypeComplete)
	}
}

func TestServer_SamplingCreateMessage(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()
	srv := NewServer("test", "1.0.0")
	srv.EnableSampling()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Initialize with client sampling capability enabled.
	initReq, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      ClientInfo{Name: "test-client", Version: "1.0.0"},
		Capabilities: Capabilities{
			Sampling: &SamplingCapability{},
		},
	})
	if err := clientTransport.Send(ctx, initReq); err != nil {
		t.Fatalf("initialize send: %v", err)
	}
	if _, err := clientTransport.Recv(ctx); err != nil {
		t.Fatalf("initialize recv: %v", err)
	}

	// Request sampling from the server side.
	resultCh := make(chan *CreateMessageResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := srv.CreateMessage(ctx, CreateMessageParams{
			Messages: []SamplingMessage{{
				Role:    "user",
				Content: Content{Type: "text", Text: "Hello"},
			}},
			MaxTokens: 32,
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- res
	}()

	// Client receives sampling/createMessage request.
	msg, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv sampling request: %v", err)
	}
	if msg.Method != "sampling/createMessage" {
		t.Fatalf("expected sampling/createMessage, got %s", msg.Method)
	}

	var params CreateMessageParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("unmarshal sampling params: %v", err)
	}
	if len(params.Messages) != 1 || params.Messages[0].Content.Text != "Hello" {
		t.Fatalf("unexpected sampling params: %+v", params)
	}

	// Client sends sampling response.
	resp, err := NewResponse(msg.ID, CreateMessageResult{
		Role:       "assistant",
		Content:    Content{Type: "text", Text: "Hi there"},
		Model:      "test-model",
		StopReason: "endTurn",
	})
	if err != nil {
		t.Fatalf("create response: %v", err)
	}
	if err := clientTransport.Send(ctx, resp); err != nil {
		t.Fatalf("send sampling response: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("CreateMessage returned error: %v", err)
	case result := <-resultCh:
		if result.Content.Text != "Hi there" {
			t.Fatalf("unexpected sampling text: %s", result.Content.Text)
		}
		if result.Model != "test-model" {
			t.Fatalf("unexpected sampling model: %s", result.Model)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for CreateMessage result")
	}
}

func TestServer_CreateMessage_ClientDoesNotSupportSampling(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()
	srv := NewServer("test", "1.0.0")
	srv.EnableSampling()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Initialize without client sampling capability.
	initReq, _ := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      ClientInfo{Name: "test-client", Version: "1.0.0"},
		Capabilities:    Capabilities{},
	})
	if err := clientTransport.Send(ctx, initReq); err != nil {
		t.Fatalf("initialize send: %v", err)
	}
	if _, err := clientTransport.Recv(ctx); err != nil {
		t.Fatalf("initialize recv: %v", err)
	}

	_, err := srv.CreateMessage(ctx, CreateMessageParams{
		Messages: []SamplingMessage{{
			Role:    "user",
			Content: Content{Type: "text", Text: "Hello"},
		}},
		MaxTokens: 32,
	})
	if err == nil {
		t.Fatal("expected error when client does not support sampling")
	}
	if !strings.Contains(err.Error(), "client does not support sampling") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServer_HandlePendingResponse_NormalizesNumericIDs(t *testing.T) {
	srv := NewServer("test", "1.0.0")
	ch := make(chan *Message, 1)

	// Simulate a pending request stored with an integer ID.
	srv.pendingRequestMu.Lock()
	srv.pendingRequests["1"] = ch
	srv.pendingRequestMu.Unlock()

	// Simulate a decoded JSON response where numeric IDs are float64.
	resp := &Message{
		JSONRPC: JSONRPCVersion,
		ID:      float64(1),
		Result:  json.RawMessage(`{"ok":true}`),
	}
	srv.handlePendingResponse(resp)

	select {
	case got := <-ch:
		if got.ID != float64(1) {
			t.Fatalf("unexpected response id: %#v", got.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for pending response")
	}
}

func TestServer_Resources_SubscribeNotifyUnsubscribe(t *testing.T) {
	clientTransport, serverTransport := NewPipeTransport()
	srv := NewServer("test", "1.0.0")

	uri := "test://known"
	srv.AddResource(Resource{URI: uri}, func(ctx context.Context, gotURI string) (*ReadResourceResult, error) {
		return TextResource(gotURI, "data"), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() { _ = srv.RunWithTransport(ctx, serverTransport) }()

	// Subscribe to resource updates.
	subReq, _ := NewRequest(1, "resources/subscribe", SubscribeResourceParams{URI: uri})
	if err := clientTransport.Send(ctx, subReq); err != nil {
		t.Fatalf("subscribe send: %v", err)
	}
	subResp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("subscribe recv: %v", err)
	}
	if subResp.Error != nil {
		t.Fatalf("subscribe returned error: %s", subResp.Error.Message)
	}

	// Notify list changed.
	if err := srv.NotifyResourcesListChanged(ctx); err != nil {
		t.Fatalf("notify list changed: %v", err)
	}
	listChanged, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv list changed: %v", err)
	}
	if listChanged.Method != "notifications/resources/list_changed" {
		t.Fatalf("unexpected list changed method: %s", listChanged.Method)
	}
	if listChanged.ID != nil {
		t.Fatalf("expected notification without id, got: %#v", listChanged.ID)
	}

	// Notify resource updated.
	if err := srv.NotifyResourceUpdated(ctx, uri); err != nil {
		t.Fatalf("notify resource updated: %v", err)
	}
	updated, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv resource updated: %v", err)
	}
	if updated.Method != "notifications/resources/updated" {
		t.Fatalf("unexpected updated method: %s", updated.Method)
	}
	var notif ResourceUpdatedNotification
	if err := json.Unmarshal(updated.Params, &notif); err != nil {
		t.Fatalf("unmarshal updated notification: %v", err)
	}
	if notif.URI != uri {
		t.Fatalf("unexpected updated URI: %s", notif.URI)
	}

	// Unsubscribe.
	unsubReq, _ := NewRequest(2, "resources/unsubscribe", UnsubscribeResourceParams{URI: uri})
	if err := clientTransport.Send(ctx, unsubReq); err != nil {
		t.Fatalf("unsubscribe send: %v", err)
	}
	unsubResp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("unsubscribe recv: %v", err)
	}
	if unsubResp.Error != nil {
		t.Fatalf("unsubscribe returned error: %s", unsubResp.Error.Message)
	}

	// Updates should no longer be delivered after unsubscribe.
	if err := srv.NotifyResourceUpdated(ctx, uri); err != nil {
		t.Fatalf("notify after unsubscribe: %v", err)
	}
	recvCtx, recvCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer recvCancel()
	if msg, err := clientTransport.Recv(recvCtx); err == nil {
		t.Fatalf("expected no post-unsubscribe notification, got method=%s id=%v", msg.Method, msg.ID)
	}
}
