![Banner](assets/banner.png)
# mcp-go

![Header](assets/header.svg)

CI (build, tests, coverage gates) runs on self-hosted GitLab; github.com/crb2nu/mcp-go mirrors `main`. Tagged releases: `v0.2.0`.

A Go SDK for building [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) servers.

MCP is a protocol for connecting AI assistants to external tools and data sources. This library provides everything you need to build MCP servers in Go.

## Protocol version

The SDK supports MCP protocol versions `2024-11-05`, `2025-06-18`, and
`2026-07-28`. During
`initialize`, a leaf server echoes a supported client version; an absent or
unknown version negotiates down to the SDK's highest supported version
available to the legacy handshake (`2025-06-18`). Streamable HTTP responses
include the negotiated
`MCP-Protocol-Version` header, while requests without that header remain
backward compatible.

MCP `2026-07-28` clients may skip `initialize` by including supported
per-request metadata in `params._meta`:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hello","arguments":{},"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"example","version":"1.0"},"clientCapabilities":{}}}}
```

The server tracks that protocol version only for the request; handlers can read
it with `mcp.ProtocolVersionFromContext(ctx)`. Legacy initialize-first flows are
unchanged. With Streamable HTTP, supported per-request metadata permits a POST
without `Mcp-Session-Id`; missing or unsupported metadata retains the legacy
session error. Stdio accepts the same request with newline-delimited or
`Content-Length` framing.

Every server implements `server/discover`. Its result contains `serverInfo`,
`supportedProtocolVersions`, `capabilities`, `tools`, and optional
`instructions`, making it suitable for the 2026-07-28 stdio compatibility
probe. Tool-call results and `tools/list`, `resources/list`, and `prompts/list`
results always include `"resultType":"complete"`, including tool error and
streaming completion results.

List responses are deterministic: tools, resources, resource templates, and
prompts are returned in lexicographic name order regardless of registration
order. Servers can opt into cache metadata with
`mcp.NewServer("name", "version", mcp.WithListCache(60_000, "server"))`.
This adds `"ttlMs":60000` and `"cacheScope":"server"` to each list response;
without the option, both fields are omitted and the server makes no cache claim.

## Features

- **Complete MCP Protocol** - Full JSON-RPC 2.0 + MCP type definitions
- **Server Framework** - Simple API for registering tools and handling requests
- **Multiple Transports** - Stdio (newline-delimited + Content-Length), WebSocket, and Streamable HTTP (JSON responses and optional SSE notifications)
- **Connection Pool** - Manage multiple MCP server connections with idle reaping
- **Production Ready** - Battle-tested with 69 production MCP servers in the Loom ecosystem
- **Examples** - Echo server, file server, and more in `examples/`

## Installation

The module path is `gitlab.flexinfer.ai/libs/mcp-go` (the canonical
self-hosted home; this GitHub repo is a mirror):

```bash
go get gitlab.flexinfer.ai/libs/mcp-go
```

Working purely from the mirror? Vendor it with a `replace` directive:

```go
// go.mod
require gitlab.flexinfer.ai/libs/mcp-go v0.2.0

replace gitlab.flexinfer.ai/libs/mcp-go => github.com/crb2nu/mcp-go v0.2.0
```

## Quick Start

```go
package main

import (
    "context"
    "os"
    "os/signal"
    "syscall"

    "gitlab.flexinfer.ai/libs/mcp-go"
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
    server := mcp.NewServer("my-server", "1.0.0")
    server.SetInstructions("A simple MCP server example")

    // Register a tool
    server.AddTool(mcp.Tool{
        Name:        "hello",
        Description: "Says hello to the given name",
        InputSchema: mcp.InputSchema{
            Type: "object",
            Properties: map[string]any{
                "name": map[string]any{
                    "type":        "string",
                    "description": "Name to greet",
                },
            },
            Required: []string{"name"},
        },
    }, func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
        name, _ := args["name"].(string)
        return mcp.TextResult("Hello, " + name + "!"), nil
    })

    // Run server (blocks until context is cancelled)
    if err := server.Run(ctx); err != nil {
        os.Exit(1)
    }
}
```

## API Reference

### Server

```go
// Create a new server
server := mcp.NewServer("name", "version")

// Set server instructions (shown to clients)
server.SetInstructions("Description of what this server does")

// Register a tool
server.AddTool(tool, handler)

// Run the server on stdio
server.Run(ctx)
```

### Tool Definition

```go
// Manual definition
mcp.Tool{
    Name:        "tool_name",
    Description: "What this tool does",
    InputSchema: mcp.InputSchema{
        Type: "object",
        Properties: map[string]any{
            "param1": map[string]any{
                "type":        "string",
                "description": "Description of param1",
            },
        },
        Required: []string{"param1"},
    },
    OutputSchema: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "key": map[string]any{"type": "string"},
        },
    },
}

// Fluent definition (Recommended)
tool := mcp.NewTool("tool_name", "What this tool does").
    WithString("param1", "Description of param1", true).
    WithNumber("param2", "Optional number", false).
    WithOutputSchema(map[string]any{
        "type": "object",
        "properties": map[string]any{
            "key": map[string]any{"type": "string"},
        },
    }).
    Build()
```

### Result Helpers

```go
// Simple text result
mcp.TextResult("Hello, world!")

// JSON object result: lossless structuredContent plus a TOON/text fallback
mcp.JSONResult(map[string]any{"key": "value"})

// Error result
mcp.ErrorResult(err)
```

`JSONResult` preserves its existing text representation for compatibility while
also returning object-shaped values in `structuredContent`. Arrays and scalar
values remain text-only because MCP defines `structuredContent` as an object.
When a tool declares an output schema, keeping the result conformant to that
schema is the tool author's responsibility.

### Transports

#### Stdio Transport (Default)

The server uses stdio transport by default. It supports both:

- Newline-delimited JSON (MCP standard)
- Content-Length framing (LSP-style)

Concurrency notes:

- `Send` is safe to call concurrently.
- `Recv` calls are serialized internally to avoid `bufio.Reader` races, but callers should still prefer a single receive loop and dispatch by `id` when building clients.

#### WebSocket Transport

For connecting to remote MCP servers:

```go
cfg := mcp.WebSocketConfig{
    URL:        "wss://mcp-hub.example.com/ws",
    ClientInfo: mcp.ClientInfo{Name: "my-client", Version: "1.0.0"},
}

transport, err := mcp.NewWebSocketTransport(ctx, cfg, "server-name")
if err != nil {
    log.Fatal(err)
}
defer transport.Close()

// Initialize the MCP connection
if err := transport.Initialize(ctx); err != nil {
    log.Fatal(err)
}

// Send/receive messages
transport.Send(ctx, msg)
msg, err := transport.Recv(ctx)
```

#### Pipe Transport (Testing)

For unit tests:

```go
client, server := mcp.NewPipeTransport()

// Messages sent on client are received on server and vice versa
client.Send(ctx, msg)
msg, _ := server.Recv(ctx)
```

## Protocol Types

The package exports all MCP protocol types:

- `Message` - JSON-RPC message
- `Error` - JSON-RPC error
- `Tool`, `InputSchema` - Tool definitions
- `CallToolParams`, `CallToolResult` - Tool call request/response
- `Resource`, `Prompt` - Resource and prompt definitions
- `Capabilities`, `ClientInfo`, `ServerInfo` - Initialization types

## License

MIT License - see [LICENSE](LICENSE)
