// Package main demonstrates a simple echo MCP server.
//
// This example shows the basic pattern for building an MCP server:
// 1. Create server with name and version
// 2. Add tools with input schemas
// 3. Run on stdio
//
// Run: go run main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Create server
	server := mcp.NewServer("echo-server", "1.0.0")
	server.SetInstructions("A simple echo server that demonstrates basic MCP patterns.")

	// Tool: echo - returns the input text
	server.AddTool(mcp.Tool{
		Name:        "echo",
		Description: "Echo back the input text",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to echo back",
				},
			},
			Required: []string{"text"},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		text, _ := args["text"].(string)
		return mcp.TextResult(text), nil
	})

	// Tool: reverse - reverses the input text
	server.AddTool(mcp.Tool{
		Name:        "reverse",
		Description: "Reverse the input text",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to reverse",
				},
			},
			Required: []string{"text"},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		text, _ := args["text"].(string)
		runes := []rune(text)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return mcp.TextResult(string(runes)), nil
	})

	// Tool: uppercase - converts text to uppercase
	server.AddTool(mcp.Tool{
		Name:        "uppercase",
		Description: "Convert text to uppercase",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to convert",
				},
			},
			Required: []string{"text"},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		text, _ := args["text"].(string)
		return mcp.TextResult(strings.ToUpper(text)), nil
	})

	// Tool: word_count - counts words in text
	server.AddTool(mcp.Tool{
		Name:        "word_count",
		Description: "Count words in text",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to count words in",
				},
			},
			Required: []string{"text"},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		text, _ := args["text"].(string)
		words := strings.Fields(text)
		return mcp.JSONResult(map[string]any{
			"count": len(words),
			"words": words,
		})
	})

	// Run server
	if err := server.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
