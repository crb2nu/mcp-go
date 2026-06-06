package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// Producers across the fleet (older mcp-go encoders, cross-version MCP hub
// payloads) emit object list items as bare indented objects with no leading
// "-" marker. The decoder must accept that form so a newer consumer can still
// read presence/session payloads from an older producer. Regression for the
// HUD "0 active agents" bug: agent_presence_list decoded from the hub failed
// with: list array "agents": expected list item (- ...), got: "agent_id: ...".
//
// The live producer keeps the dash form's 4-space field indent and simply
// drops the "-" line, so fields sit one level deeper than the dash marker.
func TestDecodeTOON_DashlessListOfObjects(t *testing.T) {
	// Faithful to the production payload: 2-line preamble, agents header at
	// indent 0, object fields at indent 2 (4 spaces), no dash. This is the
	// exact shape from the production decode error (header line 3, field
	// line 4).
	in := strings.Join([]string{
		"count: 1",
		`namespace: ""`,
		"agents[1]:",
		"    agent_id: codex-1713039686",
		"    agent_type: codex",
		"    status: active",
	}, "\n")

	v, err := DecodeTOON(in)
	if err != nil {
		t.Fatalf("dash-less decode failed: %v", err)
	}
	obj := v.(map[string]any)
	agents, ok := obj["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %#v", obj["agents"])
	}
	a0 := agents[0].(map[string]any)
	if a0["agent_id"] != "codex-1713039686" || a0["status"] != "active" {
		t.Fatalf("unexpected agent fields: %#v", a0)
	}
}

// Multi-object, heterogeneous field sets (session_id present for some agents,
// absent for others) and an empty nested array — reconstructed from a live
// agent_presence_list capture at the real 4-space field indent. Items are
// delimited only by the repeated first key (active_files), which is exactly
// what the decoder must rely on.
func TestDecodeTOON_DashlessListMultiObject(t *testing.T) {
	dashless := strings.Join([]string{
		"agents[3]:",
		"    active_files[0]:",
		"    agent_id: codex-1713039686",
		"    agent_type: codex",
		"    status: active",
		"    active_files[0]:",
		"    agent_id: claude-code-3115117524",
		"    agent_type: claude-code",
		`    description: "Claude Code · services/flexdeck"`,
		"    session_id: 66f2b05a6d504d22",
		"    status: offline",
		"    active_files[0]:",
		"    agent_id: claude-code-4140927246",
		"    agent_type: claude-code",
		"    status: active",
	}, "\n")

	v, err := DecodeTOON(dashless)
	if err != nil {
		t.Fatalf("dash-less multi-object decode failed: %v", err)
	}
	agents := v.(map[string]any)["agents"].([]any)
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d: %#v", len(agents), agents)
	}

	ids := []string{
		agents[0].(map[string]any)["agent_id"].(string),
		agents[1].(map[string]any)["agent_id"].(string),
		agents[2].(map[string]any)["agent_id"].(string),
	}
	want := []string{"codex-1713039686", "claude-code-3115117524", "claude-code-4140927246"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("agent ids mismatch:\n got=%v\nwant=%v", ids, want)
	}

	// The middle agent's extra field must land on the right object only.
	if got := agents[1].(map[string]any)["session_id"]; got != "66f2b05a6d504d22" {
		t.Fatalf("middle agent session_id = %#v, want 66f2b05a6d504d22", got)
	}
	if _, leaked := agents[0].(map[string]any)["session_id"]; leaked {
		t.Fatalf("session_id leaked into first agent: %#v", agents[0])
	}
	if _, leaked := agents[2].(map[string]any)["session_id"]; leaked {
		t.Fatalf("session_id leaked into third agent: %#v", agents[2])
	}

	// Empty nested arrays survive as empty slices, not merged away.
	if af, ok := agents[0].(map[string]any)["active_files"].([]any); !ok || len(af) != 0 {
		t.Fatalf("agent[0].active_files = %#v, want empty slice", agents[0].(map[string]any)["active_files"])
	}
}

// Some producers may indent dash-less fields at the item indent (2 spaces)
// rather than the dash form's deeper indent. The boundary detector keys off
// the first field line's actual indent, so both must decode.
func TestDecodeTOON_DashlessListShallowIndent(t *testing.T) {
	in := strings.Join([]string{
		"agents[2]:",
		"  agent_id: a",
		"  status: active",
		"  agent_id: b",
		"  status: idle",
	}, "\n")
	v, err := DecodeTOON(in)
	if err != nil {
		t.Fatalf("shallow-indent dash-less decode failed: %v", err)
	}
	agents := v.(map[string]any)["agents"].([]any)
	if len(agents) != 2 || agents[0].(map[string]any)["agent_id"] != "a" || agents[1].(map[string]any)["agent_id"] != "b" {
		t.Fatalf("shallow-indent decoded wrong: %#v", agents)
	}
}

// The canonical dash form must remain unchanged — the new compatibility branch
// only fires for the previously-rejected dash-less form.
func TestDecodeTOON_DashFormStillDecodes(t *testing.T) {
	dashed := strings.Join([]string{
		"agents[2]:",
		"  -",
		"    agent_id: a",
		"    status: active",
		"  -",
		"    agent_id: b",
		"    status: idle",
	}, "\n")
	v, err := DecodeTOON(dashed)
	if err != nil {
		t.Fatalf("dash form regressed: %v", err)
	}
	agents := v.(map[string]any)["agents"].([]any)
	if len(agents) != 2 || agents[0].(map[string]any)["agent_id"] != "a" || agents[1].(map[string]any)["agent_id"] != "b" {
		t.Fatalf("dash form decoded wrong: %#v", agents)
	}
}

// The encoder emits the dash form; round-tripping its own output must keep
// working (guards against the compatibility branch shadowing the dash path).
func TestDecodeTOON_EncoderRoundTripUnaffected(t *testing.T) {
	src := map[string]any{
		"agents": []any{
			map[string]any{"agent_id": "a", "active_files": []any{}, "status": "active"},
			map[string]any{"agent_id": "b", "active_files": []any{"x.go"}, "status": "idle"},
		},
	}
	encoded, err := formatTOON(src)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	decoded, err := DecodeTOON(encoded)
	if err != nil {
		t.Fatalf("round-trip decode failed for:\n%s\nerr=%v", encoded, err)
	}
	if len(decoded.(map[string]any)["agents"].([]any)) != 2 {
		t.Fatalf("round-trip lost agents:\n%s\n=> %#v", encoded, decoded)
	}
}
