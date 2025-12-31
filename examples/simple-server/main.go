// Example MCP server demonstrating basic tool registration.
//
// Run with: go run main.go
// Then connect with an MCP client (e.g., Example Client, VS Code extension).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Create the MCP server
	server := mcp.NewServer("example-server", "1.0.0")
	server.SetInstructions("A simple example MCP server with greeting and time tools.")

	// Register a greeting tool
	server.AddTool(mcp.Tool{
		Name:        "greet",
		Description: "Greets a person by name",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The name of the person to greet",
				},
			},
			Required: []string{"name"},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		name, ok := args["name"].(string)
		if !ok {
			return mcp.ErrorResult(fmt.Errorf("name must be a string")), nil
		}
		return mcp.TextResult(fmt.Sprintf("Hello, %s! Welcome to the MCP server.", name)), nil
	})

	// Register a time tool
	server.AddTool(mcp.Tool{
		Name:        "current_time",
		Description: "Returns the current time in the specified timezone",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"timezone": map[string]any{
					"type":        "string",
					"description": "Timezone name (e.g., 'America/New_York', 'UTC'). Default: UTC",
				},
			},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		tz := "UTC"
		if t, ok := args["timezone"].(string); ok && t != "" {
			tz = t
		}

		loc, err := time.LoadLocation(tz)
		if err != nil {
			return mcp.ErrorResult(fmt.Errorf("invalid timezone: %s", tz)), nil
		}

		now := time.Now().In(loc)
		return mcp.TextResult(fmt.Sprintf("Current time in %s: %s", tz, now.Format(time.RFC1123))), nil
	})

	// Register a calculator tool
	server.AddTool(mcp.Tool{
		Name:        "calculate",
		Description: "Performs basic arithmetic operations",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"operation": map[string]any{
					"type":        "string",
					"description": "The operation to perform: add, subtract, multiply, divide",
					"enum":        []string{"add", "subtract", "multiply", "divide"},
				},
				"a": map[string]any{
					"type":        "number",
					"description": "First operand",
				},
				"b": map[string]any{
					"type":        "number",
					"description": "Second operand",
				},
			},
			Required: []string{"operation", "a", "b"},
		},
	}, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		op, _ := args["operation"].(string)
		a, _ := args["a"].(float64)
		b, _ := args["b"].(float64)

		var result float64
		switch op {
		case "add":
			result = a + b
		case "subtract":
			result = a - b
		case "multiply":
			result = a * b
		case "divide":
			if b == 0 {
				return mcp.ErrorResult(fmt.Errorf("division by zero")), nil
			}
			result = a / b
		default:
			return mcp.ErrorResult(fmt.Errorf("unknown operation: %s", op)), nil
		}

		return mcp.TextResult(fmt.Sprintf("%.6g", result)), nil
	})

	// Run the server
	if err := server.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
