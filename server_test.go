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

	go srv.RunWithTransport(ctx, serverTransport)

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

	go srv.RunWithTransport(ctx, serverTransport)

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

	go srv.RunWithTransport(ctx, serverTransport)

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
	if result.Capabilities.Prompts == nil {
		t.Error("expected prompts capability")
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

	go srv.RunWithTransport(ctx, serverTransport)

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

	go srv.RunWithTransport(ctx, serverTransport)

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

	go srv.RunWithTransport(ctx, serverTransport)

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
}
