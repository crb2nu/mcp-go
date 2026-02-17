package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestServer_StdioIntegration_SamplingAndResourceSubscriptions(t *testing.T) {
	clientTransport, serverTransport, cleanup := newStdioTransportPair(t)
	defer cleanup()

	server := NewServer("test-server", "1.0.0")
	server.EnableSampling()
	server.AddResource(Resource{
		URI:  "memo://daily",
		Name: "daily memo",
	}, func(_ context.Context, uri string) (*ReadResourceResult, error) {
		return &ReadResourceResult{
			Contents: []ResourceContents{{
				URI:  uri,
				Text: "hello",
			}},
		}, nil
	})

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.RunWithTransport(serverCtx, serverTransport)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initReq, err := NewRequest(1, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities: Capabilities{
			Sampling: &SamplingCapability{},
			Resources: &ResourcesCapability{
				Subscribe:   true,
				ListChanged: true,
			},
		},
		ClientInfo: ClientInfo{Name: "test-client", Version: "1.0"},
	})
	if err != nil {
		t.Fatalf("create initialize request: %v", err)
	}
	if err := clientTransport.Send(ctx, initReq); err != nil {
		t.Fatalf("send initialize: %v", err)
	}

	initResp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv initialize response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize response error: %+v", initResp.Error)
	}
	var initResult InitializeResult
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if initResult.Capabilities.Sampling == nil {
		t.Fatal("expected server sampling capability in initialize response")
	}
	if initResult.Capabilities.Resources == nil || !initResult.Capabilities.Resources.Subscribe {
		t.Fatal("expected server resource subscribe capability in initialize response")
	}

	initialized, err := NewRequest(nil, "notifications/initialized", nil)
	if err != nil {
		t.Fatalf("create initialized notification: %v", err)
	}
	if err := clientTransport.Send(ctx, initialized); err != nil {
		t.Fatalf("send initialized notification: %v", err)
	}

	subReq, err := NewRequest(2, "resources/subscribe", SubscribeResourceParams{URI: "memo://daily"})
	if err != nil {
		t.Fatalf("create subscribe request: %v", err)
	}
	if err := clientTransport.Send(ctx, subReq); err != nil {
		t.Fatalf("send subscribe request: %v", err)
	}
	subResp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv subscribe response: %v", err)
	}
	if subResp.Error != nil {
		t.Fatalf("subscribe response error: %+v", subResp.Error)
	}

	if err := server.NotifyResourcesListChanged(ctx); err != nil {
		t.Fatalf("notify resources list changed: %v", err)
	}
	listChanged, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv list changed notification: %v", err)
	}
	if listChanged.Method != "notifications/resources/list_changed" {
		t.Fatalf("expected list changed notification, got method %q", listChanged.Method)
	}

	if err := server.NotifyResourceUpdated(ctx, "memo://daily"); err != nil {
		t.Fatalf("notify resource updated: %v", err)
	}
	updated, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv resource updated notification: %v", err)
	}
	if updated.Method != "notifications/resources/updated" {
		t.Fatalf("expected resource updated notification, got method %q", updated.Method)
	}

	unsubReq, err := NewRequest(3, "resources/unsubscribe", UnsubscribeResourceParams{URI: "memo://daily"})
	if err != nil {
		t.Fatalf("create unsubscribe request: %v", err)
	}
	if err := clientTransport.Send(ctx, unsubReq); err != nil {
		t.Fatalf("send unsubscribe request: %v", err)
	}
	unsubResp, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv unsubscribe response: %v", err)
	}
	if unsubResp.Error != nil {
		t.Fatalf("unsubscribe response error: %+v", unsubResp.Error)
	}

	if err := server.NotifyResourceUpdated(ctx, "memo://daily"); err != nil {
		t.Fatalf("post-unsubscribe notify call: %v", err)
	}

	toolsReq, err := NewRequest(4, "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("create tools/list request: %v", err)
	}
	if err := clientTransport.Send(ctx, toolsReq); err != nil {
		t.Fatalf("send tools/list request: %v", err)
	}
	nextMsg, err := clientTransport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv message after unsubscribe: %v", err)
	}
	if nextMsg.Method != "" || nextMsg.ID != float64(4) {
		t.Fatalf("expected tools/list response after unsubscribe, got method=%q id=%v", nextMsg.Method, nextMsg.ID)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		samplingReq, recvErr := clientTransport.Recv(ctx)
		if recvErr != nil {
			t.Errorf("recv sampling request: %v", recvErr)
			return
		}
		if samplingReq.Method != "sampling/createMessage" {
			t.Errorf("expected sampling/createMessage, got %q", samplingReq.Method)
			return
		}
		resp, createErr := NewResponse(samplingReq.ID, CreateMessageResult{
			Role:       "assistant",
			Content:    Content{Type: "text", Text: "integration-ok"},
			Model:      "test-model",
			StopReason: "endTurn",
		})
		if createErr != nil {
			t.Errorf("create sampling response: %v", createErr)
			return
		}
		if sendErr := clientTransport.Send(ctx, resp); sendErr != nil {
			t.Errorf("send sampling response: %v", sendErr)
		}
	}()

	result, err := server.CreateMessage(ctx, CreateMessageParams{
		Messages: []SamplingMessage{{
			Role: "user",
			Content: Content{
				Type: "text",
				Text: "hello",
			},
		}},
		MaxTokens: 32,
	})
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if result.Content.Text != "integration-ok" {
		t.Fatalf("unexpected sampling content: %q", result.Content.Text)
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for sampling responder goroutine")
	}

	serverCancel()
	cleanup()

	select {
	case err := <-serverErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("server exited with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func newStdioTransportPair(t *testing.T) (*StdioTransport, *StdioTransport, func()) {
	t.Helper()

	c2sReader, c2sWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe c2s: %v", err)
	}
	s2cReader, s2cWriter, err := os.Pipe()
	if err != nil {
		_ = c2sReader.Close()
		_ = c2sWriter.Close()
		t.Fatalf("pipe s2c: %v", err)
	}

	closed := false
	cleanup := func() {
		if closed {
			return
		}
		closed = true
		_ = c2sReader.Close()
		_ = c2sWriter.Close()
		_ = s2cReader.Close()
		_ = s2cWriter.Close()
	}

	client := NewStdioTransport(s2cReader, c2sWriter)
	server := NewStdioTransport(c2sReader, s2cWriter)
	return client, server, cleanup
}
