// Package main demonstrates a file system MCP server.
//
// This example shows patterns for:
// - Handling file operations safely
// - Validating paths to prevent directory traversal
// - Returning structured JSON results
//
// Usage: MCP_FILES_ROOT=/path/to/files go run main.go
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	mcp "gitlab.flexinfer.ai/libs/mcp-go"
)

type fileServer struct {
	rootDir string
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Get root directory from environment
	rootDir := os.Getenv("MCP_FILES_ROOT")
	if rootDir == "" {
		rootDir = "."
	}
	rootDir, _ = filepath.Abs(rootDir)

	fs := &fileServer{rootDir: rootDir}

	server := mcp.NewServer("file-server", "1.0.0")
	server.SetInstructions(fmt.Sprintf("File server with root at %s", rootDir))

	// list_directory
	server.AddTool(mcp.Tool{
		Name:        "list_directory",
		Description: "List contents of a directory",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to list (empty for root)",
				},
			},
		},
	}, fs.handleListDir)

	// read_file
	server.AddTool(mcp.Tool{
		Name:        "read_file",
		Description: "Read contents of a file",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path to read",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"description": "Maximum bytes to read (default: 1MB)",
				},
			},
			Required: []string{"path"},
		},
	}, fs.handleReadFile)

	// file_info
	server.AddTool(mcp.Tool{
		Name:        "file_info",
		Description: "Get information about a file or directory",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative path",
				},
			},
			Required: []string{"path"},
		},
	}, fs.handleFileInfo)

	// search_files
	server.AddTool(mcp.Tool{
		Name:        "search_files",
		Description: "Search for files matching a pattern",
		InputSchema: mcp.InputSchema{
			Type: "object",
			Properties: map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern to match (e.g., '*.go', '**/*.md')",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Starting directory (empty for root)",
				},
			},
			Required: []string{"pattern"},
		},
	}, fs.handleSearchFiles)

	if err := server.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// safePath validates and resolves a path within the root directory.
func (f *fileServer) safePath(relPath string) (string, error) {
	// Clean and join path
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}

	fullPath := filepath.Join(f.rootDir, cleaned)
	fullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}

	// Ensure path is within root
	if !strings.HasPrefix(fullPath, f.rootDir) {
		return "", fmt.Errorf("path outside root directory")
	}

	return fullPath, nil
}

func (f *fileServer) handleListDir(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	relPath, _ := args["path"].(string)
	if relPath == "" {
		relPath = "."
	}

	fullPath, err := f.safePath(relPath)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	var items []map[string]any
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		items = append(items, map[string]any{
			"name":    entry.Name(),
			"isDir":   entry.IsDir(),
			"size":    info.Size(),
			"modTime": info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	return mcp.JSONResult(map[string]any{
		"path":  relPath,
		"items": items,
		"count": len(items),
	})
}

func (f *fileServer) handleReadFile(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	relPath, _ := args["path"].(string)
	maxBytes := 1024 * 1024 // 1MB default
	if mb, ok := args["max_bytes"].(float64); ok {
		maxBytes = int(mb)
	}

	fullPath, err := f.safePath(relPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cannot read directory as file")
	}
	if info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("file too large (%d bytes > %d max)", info.Size(), maxBytes)
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}

	return mcp.TextResult(string(content)), nil
}

func (f *fileServer) handleFileInfo(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	relPath, _ := args["path"].(string)

	fullPath, err := f.safePath(relPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}

	return mcp.JSONResult(map[string]any{
		"name":    info.Name(),
		"path":    relPath,
		"isDir":   info.IsDir(),
		"size":    info.Size(),
		"mode":    info.Mode().String(),
		"modTime": info.ModTime().Format("2006-01-02 15:04:05"),
	})
}

func (f *fileServer) handleSearchFiles(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	pattern, _ := args["pattern"].(string)
	relPath, _ := args["path"].(string)
	if relPath == "" {
		relPath = "."
	}

	startPath, err := f.safePath(relPath)
	if err != nil {
		return nil, err
	}

	var matches []string
	err = filepath.WalkDir(startPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Get relative path for matching
		rel, _ := filepath.Rel(startPath, path)

		matched, _ := filepath.Match(pattern, d.Name())
		if matched {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Limit results
	if len(matches) > 100 {
		matches = matches[:100]
	}

	return mcp.JSONResult(map[string]any{
		"pattern": pattern,
		"matches": matches,
		"count":   len(matches),
	})
}
