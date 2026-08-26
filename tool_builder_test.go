package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolBuilder(t *testing.T) {
	tool := NewTool("calculate", "Calculate something").
		WithString("operation", "The operation to perform", true).
		WithNumber("x", "First number", true).
		WithNumber("y", "Second number", true).
		WithBoolean("verbose", "Verbose output", false).
		WithEnum("mode", "Calculation mode", []string{"fast", "precise"}, false).
		Build()

	if tool.Name != "calculate" {
		t.Errorf("Expected name 'calculate', got '%s'", tool.Name)
	}

	if tool.InputSchema.Type != "object" {
		t.Errorf("Expected type 'object', got '%s'", tool.InputSchema.Type)
	}

	props := tool.InputSchema.Properties
	if len(props) != 5 {
		t.Errorf("Expected 5 properties, got %d", len(props))
	}

	if len(tool.InputSchema.Required) != 3 {
		t.Errorf("Expected 3 required fields, got %d", len(tool.InputSchema.Required))
	}

	// Verify JSON structure
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Failed to marshal tool: %v", err)
	}
	t.Logf("Tool JSON: %s", string(b))
}

func TestToolBuilder_WithOutputSchema(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "string"}},
	}
	tool := NewTool("answer", "Return an answer").WithOutputSchema(schema).Build()
	if tool.OutputSchema["type"] != "object" {
		t.Fatalf("output schema was not retained: %#v", tool.OutputSchema)
	}
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	if !strings.Contains(string(b), `"outputSchema":{"properties"`) {
		t.Fatalf("outputSchema missing from tool JSON: %s", b)
	}
}

func TestToolBuilder_LegacyToolOmitsOutputSchema(t *testing.T) {
	tool := NewTool("legacy", "Legacy tool").WithString("value", "Value", false).Build()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	if strings.Contains(string(b), "outputSchema") {
		t.Fatalf("legacy tool unexpectedly serialized outputSchema: %s", b)
	}
}
