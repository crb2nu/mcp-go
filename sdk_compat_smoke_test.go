package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

func TestSDKCompatibilitySmokeInitializeToolsAndCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientTransport, serverTransport := mcp.NewPipeTransport()
	server := mcp.NewServer("compat-smoke", "0.1.0")
	server.SetInstructions("SDK compatibility smoke server")
	server.AddTool(
		mcp.NewTool("echo", "Echo a text value").
			WithString("text", "Text to echo", true).
			Build(),
		func(_ context.Context, args map[string]any) (*mcp.CallToolResult, error) {
			text, _ := args["text"].(string)
			return mcp.TextResult("echo: " + text), nil
		},
	)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.RunWithTransport(serverCtx, serverTransport)
	}()
	t.Cleanup(func() {
		serverCancel()
		_ = clientTransport.Close()
		_ = serverTransport.Close()
		select {
		case err := <-serverErr:
			if err != nil && err != context.Canceled {
				t.Fatalf("server exited with error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for server shutdown")
		}
	})

	initialize := sendSDKSmokeRequest[mcp.InitializeResult](t, ctx, clientTransport, 1, "initialize", mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    mcp.Capabilities{},
		ClientInfo:      mcp.ClientInfo{Name: "sdk-compat-smoke", Version: "0.1.0"},
	})
	if initialize.ProtocolVersion != mcp.ProtocolVersion {
		t.Fatalf("initialize protocolVersion = %q, want %q", initialize.ProtocolVersion, mcp.ProtocolVersion)
	}
	if initialize.ServerInfo.Name != "compat-smoke" || initialize.ServerInfo.Version != "0.1.0" {
		t.Fatalf("unexpected serverInfo: %+v", initialize.ServerInfo)
	}
	if initialize.Capabilities.Tools == nil {
		t.Fatal("initialize response did not advertise tools capability")
	}
	if initialize.Instructions != "SDK compatibility smoke server" {
		t.Fatalf("initialize instructions = %q", initialize.Instructions)
	}

	initialized, err := mcp.NewRequest(nil, "notifications/initialized", nil)
	if err != nil {
		t.Fatalf("create initialized notification: %v", err)
	}
	if err := clientTransport.Send(ctx, initialized); err != nil {
		t.Fatalf("send initialized notification: %v", err)
	}

	list := sendSDKSmokeRequest[mcp.ToolsListResult](t, ctx, clientTransport, 2, "tools/list", map[string]any{})
	if len(list.Tools) != 1 {
		t.Fatalf("tools/list returned %d tools, want 1", len(list.Tools))
	}
	if list.Tools[0].Name != "echo" {
		t.Fatalf("tool name = %q, want echo", list.Tools[0].Name)
	}
	if list.Tools[0].InputSchema.Type != "object" {
		t.Fatalf("tool input schema type = %q, want object", list.Tools[0].InputSchema.Type)
	}
	if fmt.Sprint(list.Tools[0].InputSchema.Properties["text"]) == "" {
		t.Fatal("tool input schema is missing text property")
	}

	call := sendSDKSmokeRequest[mcp.CallToolResult](t, ctx, clientTransport, 3, "tools/call", mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "hello"},
	})
	if call.IsError {
		t.Fatalf("tools/call returned IsError: %+v", call)
	}
	if len(call.Content) != 1 || call.Content[0].Type != "text" || call.Content[0].Text != "echo: hello" {
		t.Fatalf("unexpected tools/call content: %+v", call.Content)
	}
}

func TestSDKCompatibilitySmokeStdioFramingBasics(t *testing.T) {
	t.Run("newline delimited JSON", func(t *testing.T) {
		var out bytes.Buffer
		transport := mcp.NewStdioTransport(
			bytes.NewBufferString(`{"jsonrpc":"2.0","id":"line-1","method":"ping"}`+"\n"),
			&out,
		)
		t.Cleanup(func() { _ = transport.Close() })

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		msg, err := transport.Recv(ctx)
		if err != nil {
			t.Fatalf("Recv() error: %v", err)
		}
		if msg.Method != "ping" || fmt.Sprint(msg.ID) != "line-1" {
			t.Fatalf("received message = method %q id %v", msg.Method, msg.ID)
		}

		resp, err := mcp.NewResponse(msg.ID, map[string]bool{"ok": true})
		if err != nil {
			t.Fatalf("create response: %v", err)
		}
		if err := transport.Send(ctx, resp); err != nil {
			t.Fatalf("Send() error: %v", err)
		}
		if !strings.HasSuffix(out.String(), "\n") {
			t.Fatalf("newline transport output %q does not end with newline", out.String())
		}
		if strings.HasPrefix(out.String(), "Content-Length:") {
			t.Fatalf("newline transport unexpectedly emitted Content-Length framing: %q", out.String())
		}
	})

	t.Run("Content-Length mirror", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":"cl-1","method":"ping"}`
		framed := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
		var out bytes.Buffer
		transport := mcp.NewStdioTransport(bytes.NewBufferString(framed), &out)
		t.Cleanup(func() { _ = transport.Close() })

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		msg, err := transport.Recv(ctx)
		if err != nil {
			t.Fatalf("Recv() error: %v", err)
		}
		resp, err := mcp.NewResponse(msg.ID, map[string]bool{"ok": true})
		if err != nil {
			t.Fatalf("create response: %v", err)
		}
		if err := transport.Send(ctx, resp); err != nil {
			t.Fatalf("Send() error: %v", err)
		}
		if !strings.HasPrefix(out.String(), "Content-Length: ") {
			t.Fatalf("Content-Length transport output missing header: %q", out.String())
		}
		if !strings.Contains(out.String(), "\r\n\r\n") {
			t.Fatalf("Content-Length transport output missing header separator: %q", out.String())
		}
	})
}

func sendSDKSmokeRequest[T any](t *testing.T, ctx context.Context, transport mcp.Transport, id int64, method string, params any) T {
	t.Helper()

	req, err := mcp.NewRequest(id, method, params)
	if err != nil {
		t.Fatalf("create %s request: %v", method, err)
	}
	if err := transport.Send(ctx, req); err != nil {
		t.Fatalf("send %s request: %v", method, err)
	}

	resp, err := transport.Recv(ctx)
	if err != nil {
		t.Fatalf("recv %s response: %v", method, err)
	}
	if resp.Error != nil {
		t.Fatalf("%s response error: %+v", method, resp.Error)
	}
	if fmt.Sprint(resp.ID) != fmt.Sprint(id) {
		t.Fatalf("%s response id = %v, want %d", method, resp.ID, id)
	}

	var result T
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal %s response: %v", method, err)
	}
	return result
}
