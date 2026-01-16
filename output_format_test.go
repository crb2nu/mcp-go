package mcp

import (
	"strings"
	"testing"
)

func TestJSONResult_DefaultsToTOON(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "")

	payload := map[string]any{
		"users": []any{
			map[string]any{"id": 1, "name": "Alice", "role": "admin"},
			map[string]any{"id": 2, "name": "Bob", "role": "user"},
		},
	}

	res, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("unexpected content length: %d", len(res.Content))
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "users[2]{id,name,role}:") {
		t.Fatalf("expected TOON tabular header, got: %q", got)
	}
}

func TestJSONResult_JSONPretty(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "json_pretty")

	payload := map[string]any{"ok": true, "count": 2}

	res, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult: %v", err)
	}
	got := res.Content[0].Text
	if !strings.HasPrefix(got, "{") || !strings.Contains(got, "\n  ") {
		t.Fatalf("expected indented JSON, got: %q", got)
	}
}

func TestJSONResult_AutoChoosesSmaller(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "auto")

	payload := map[string]any{
		"users": []any{
			map[string]any{"id": 1, "name": "Alice", "role": "admin"},
			map[string]any{"id": 2, "name": "Bob", "role": "user"},
		},
	}

	res, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult: %v", err)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "users[2]{id,name,role}:") {
		t.Fatalf("expected TOON in auto mode for tabular data, got: %q", got)
	}
}

func TestJSONResult_TOONFallbacksToJSONOnUnsupportedString(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "toon")

	payload := map[string]any{"bad": string(rune(0x01))}

	res, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult: %v", err)
	}
	got := res.Content[0].Text
	if strings.HasPrefix(got, "{") {
		t.Fatalf("expected TOON encoding (no JSON fallback), got: %q", got)
	}
	if !strings.Contains(got, `bad: "\u0001"`) {
		t.Fatalf("expected unicode escape for control char, got: %q", got)
	}
}

func TestEstimateTokensApprox(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "json_pretty")

	payload := map[string]any{
		"users": []any{
			map[string]any{"id": 1, "name": "Alice", "role": "admin"},
			map[string]any{"id": 2, "name": "Bob", "role": "user"},
		},
	}

	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "json_pretty")
	jsonPrettyRes, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult json_pretty: %v", err)
	}

	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "toon")
	toonRes, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult toon: %v", err)
	}

	jsonPrettyTokens := EstimateTokensApprox(jsonPrettyRes.Content[0].Text)
	toonTokens := EstimateTokensApprox(toonRes.Content[0].Text)
	if toonTokens >= jsonPrettyTokens {
		t.Fatalf("expected TOON to be smaller than pretty JSON (approx), got toon=%d json_pretty=%d", toonTokens, jsonPrettyTokens)
	}
}

func TestTOON_DeepConvertsStdoutJSON(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "toon")

	payload := map[string]any{
		"stdout": `{"ok":true,"items":[{"id":1},{"id":2}]}`,
	}

	res, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult: %v", err)
	}
	got := res.Content[0].Text

	// Embedded JSON should be decoded into nested TOON, not kept as a quoted JSON string.
	if strings.Contains(got, `"{"`) || strings.Contains(got, `\"ok\"`) {
		t.Fatalf("expected embedded JSON to be decoded, got: %q", got)
	}
	if !strings.Contains(got, "stdout:") || !strings.Contains(got, "ok: true") || !strings.Contains(got, "items[2]{id}:") {
		t.Fatalf("expected decoded stdout object, got: %q", got)
	}
}

func TestTOON_DoesNotDeepConvertContentField(t *testing.T) {
	t.Setenv("LOOM_MCP_OUTPUT_FORMAT", "toon")

	payload := map[string]any{
		"content": `{"ok":true}`,
	}

	res, err := JSONResult(payload)
	if err != nil {
		t.Fatalf("JSONResult: %v", err)
	}
	got := res.Content[0].Text

	// For content-like fields, preserve original string (commonly file contents).
	if strings.Contains(got, "\nok: true") || strings.Contains(got, "\n  ok: true") {
		t.Fatalf("expected content field not to be deep-converted, got: %q", got)
	}
}
