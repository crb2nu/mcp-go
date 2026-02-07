package mcp

import (
	"encoding/json"
	"testing"
)

func TestDecodeTOON_SimpleKeyValues(t *testing.T) {
	input := map[string]any{
		"name":    "alice",
		"age":     42,
		"active":  true,
		"deleted": false,
		"score":   3.14,
		"note":    nil,
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_NestedObject(t *testing.T) {
	input := map[string]any{
		"user": map[string]any{
			"name": "bob",
			"age":  30,
			"address": map[string]any{
				"city":  "NYC",
				"state": "NY",
			},
		},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_TabularArray(t *testing.T) {
	input := map[string]any{
		"users": []any{
			map[string]any{"id": 1, "name": "Alice", "role": "admin"},
			map[string]any{"id": 2, "name": "Bob", "role": "user"},
		},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_InlinePrimitiveArray(t *testing.T) {
	input := map[string]any{
		"tags": []any{"go", "mcp", "toon"},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_InlineNumberArray(t *testing.T) {
	input := map[string]any{
		"scores": []any{1, 2, 3},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_EmptyObject(t *testing.T) {
	input := map[string]any{
		"config": map[string]any{},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_EmptyArray(t *testing.T) {
	input := map[string]any{
		"items": []any{},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_QuotedStrings(t *testing.T) {
	input := map[string]any{
		"greeting": "hello world",
		"path":     "/usr/local/bin",
		"empty":    "",
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_StringEscapes(t *testing.T) {
	input := map[string]any{
		"newline": "line1\nline2",
		"tab":     "col1\tcol2",
		"quote":   `she said "hello"`,
		"back":    `back\slash`,
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_MixedComplex(t *testing.T) {
	input := map[string]any{
		"server": map[string]any{
			"host": "localhost",
			"port": 8080,
		},
		"users": []any{
			map[string]any{"id": 1, "name": "Alice"},
			map[string]any{"id": 2, "name": "Bob"},
		},
		"debug":   false,
		"version": "1.0.0",
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_ListArrayWithObjects(t *testing.T) {
	// Test list array form: objects that have ONLY nested fields (no primitive fields at all)
	// cannot be tabularized even with primitive subset, so the encoder uses list form.
	input := map[string]any{
		"items": []any{
			map[string]any{
				"config": map[string]any{"host": "a"},
			},
			map[string]any{
				"config": map[string]any{"host": "b"},
			},
		},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_TabularPrimitiveSubsetIsLossy(t *testing.T) {
	// The encoder's tabularizePrimitiveSubset drops non-primitive fields.
	// Verify that the decoder correctly recovers the primitive subset the encoder outputs.
	input := map[string]any{
		"items": []any{
			map[string]any{
				"id": 1,
				"meta": map[string]any{
					"created": "2024-01-01",
				},
			},
			map[string]any{
				"id": 2,
				"meta": map[string]any{
					"created": "2024-01-02",
				},
			},
		},
	}

	toon, err := formatTOON(input)
	if err != nil {
		t.Fatalf("formatTOON: %v", err)
	}

	t.Logf("TOON output:\n%s", toon)

	decoded, err := DecodeTOON(toon)
	if err != nil {
		t.Fatalf("DecodeTOON: %v\nTOON:\n%s", err, toon)
	}

	// The decoder should recover only the primitive fields (id), not meta.
	expected := map[string]any{
		"items": []any{
			map[string]any{"id": float64(1)},
			map[string]any{"id": float64(2)},
		},
	}

	assertJSONEqual(t, expected, decoded)
}

func TestDecodeTOON_NullValue(t *testing.T) {
	input := map[string]any{
		"value": nil,
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_BooleanValues(t *testing.T) {
	input := map[string]any{
		"on":  true,
		"off": false,
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_NumberTypes(t *testing.T) {
	input := map[string]any{
		"integer": 42,
		"neg":     -7,
		"decimal": 3.14,
		"zero":    0,
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_StringThatLooksLikeNumber(t *testing.T) {
	// The encoder quotes strings that look like numbers.
	input := map[string]any{
		"port": "8080",
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_StringThatLooksLikeBool(t *testing.T) {
	// The encoder quotes strings that look like booleans.
	input := map[string]any{
		"flag": "true",
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_StringThatLooksLikeNull(t *testing.T) {
	input := map[string]any{
		"val": "null",
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_EmptyString(t *testing.T) {
	input := map[string]any{
		"empty": "",
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_TabularWithQuotedValues(t *testing.T) {
	input := map[string]any{
		"rows": []any{
			map[string]any{"name": "hello world", "count": 1},
			map[string]any{"name": "foo bar", "count": 2},
		},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_DeeplyNested(t *testing.T) {
	input := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": map[string]any{
					"d": "deep",
				},
			},
		},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOONToJSON(t *testing.T) {
	input := map[string]any{
		"name": "test",
		"ok":   true,
	}

	toon, err := formatTOON(input)
	if err != nil {
		t.Fatalf("formatTOON: %v", err)
	}

	jsonBytes, err := DecodeTOONToJSON(toon)
	if err != nil {
		t.Fatalf("DecodeTOONToJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(jsonBytes, &got); err != nil {
		t.Fatalf("json.Unmarshal decoded JSON: %v", err)
	}

	expectedJSON, _ := json.Marshal(input)
	var expected map[string]any
	json.Unmarshal(expectedJSON, &expected)

	assertJSONEqual(t, expected, got)
}

func TestDecodeTOON_ControlCharEscape(t *testing.T) {
	input := map[string]any{
		"val": string(rune(0x01)),
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_SpecificTOONStrings(t *testing.T) {
	tests := []struct {
		name     string
		toon     string
		expected any
	}{
		{
			name:     "simple key value",
			toon:     "name: alice",
			expected: map[string]any{"name": "alice"},
		},
		{
			name:     "number value",
			toon:     "count: 42",
			expected: map[string]any{"count": float64(42)},
		},
		{
			name:     "bool value",
			toon:     "ok: true",
			expected: map[string]any{"ok": true},
		},
		{
			name:     "null value",
			toon:     "val: null",
			expected: map[string]any{"val": nil},
		},
		{
			name:     "quoted string",
			toon:     `msg: "hello world"`,
			expected: map[string]any{"msg": "hello world"},
		},
		{
			name:     "empty string",
			toon:     `empty: ""`,
			expected: map[string]any{"empty": ""},
		},
		{
			name: "nested object",
			toon: "parent:\n  child: value",
			expected: map[string]any{
				"parent": map[string]any{
					"child": "value",
				},
			},
		},
		{
			name: "empty object",
			toon: "config:",
			expected: map[string]any{
				"config": map[string]any{},
			},
		},
		{
			name: "empty array",
			toon: "items[0]:",
			expected: map[string]any{
				"items": []any{},
			},
		},
		{
			name: "inline array",
			toon: "tags[3]: go,mcp,toon",
			expected: map[string]any{
				"tags": []any{"go", "mcp", "toon"},
			},
		},
		{
			name: "tabular array",
			toon: "users[2]{id,name}:\n  1,Alice\n  2,Bob",
			expected: map[string]any{
				"users": []any{
					map[string]any{"id": float64(1), "name": "Alice"},
					map[string]any{"id": float64(2), "name": "Bob"},
				},
			},
		},
		{
			name: "list array with primitives",
			toon: "items[2]:\n  - hello\n  - world",
			expected: map[string]any{
				"items": []any{"hello", "world"},
			},
		},
		{
			name: "list array with objects",
			toon: "items[2]:\n  -\n    name: alice\n    age: 30\n  -\n    name: bob\n    age: 25",
			expected: map[string]any{
				"items": []any{
					map[string]any{"name": "alice", "age": float64(30)},
					map[string]any{"name": "bob", "age": float64(25)},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeTOON(tt.toon)
			if err != nil {
				t.Fatalf("DecodeTOON: %v", err)
			}
			assertJSONEqual(t, tt.expected, got)
		})
	}
}

func TestDecodeTOON_TabularWithNullAndBool(t *testing.T) {
	input := map[string]any{
		"data": []any{
			map[string]any{"id": 1, "active": true, "note": nil},
			map[string]any{"id": 2, "active": false, "note": nil},
		},
	}
	assertRoundTrip(t, input)
}

func TestDecodeTOON_InlineArrayWithMixedTypes(t *testing.T) {
	input := map[string]any{
		"mixed": []any{1, "two", true, nil},
	}
	assertRoundTrip(t, input)
}

// assertRoundTrip encodes v to TOON, decodes it back, and compares via JSON.
func assertRoundTrip(t *testing.T, v any) {
	t.Helper()

	toon, err := formatTOON(v)
	if err != nil {
		t.Fatalf("formatTOON: %v", err)
	}

	t.Logf("TOON output:\n%s", toon)

	decoded, err := DecodeTOON(toon)
	if err != nil {
		t.Fatalf("DecodeTOON: %v\nTOON:\n%s", err, toon)
	}

	// Normalize both through JSON for comparison.
	expectedJSON, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal original: %v", err)
	}

	gotJSON, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("json.Marshal decoded: %v", err)
	}

	// Parse both into generic structures for deep comparison.
	var expected, got any
	json.Unmarshal(expectedJSON, &expected)
	json.Unmarshal(gotJSON, &got)

	assertJSONEqual(t, expected, got)
}

// assertJSONEqual compares two values by their JSON representation.
func assertJSONEqual(t *testing.T, expected, got any) {
	t.Helper()

	expectedJSON, _ := json.Marshal(expected)
	gotJSON, _ := json.Marshal(got)

	if string(expectedJSON) != string(gotJSON) {
		t.Errorf("mismatch:\n  expected: %s\n  got:      %s", expectedJSON, gotJSON)
	}
}
