package fake

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

var update = flag.Bool("update", false, "rewrite testdata/web-protocol from the fake scripts")

// conformanceDir holds the web protocol fixtures aide vendors
// (testdata/web-protocol/README.md).
const conformanceDir = "../../../testdata/web-protocol"

// conformance is one fixture: what the scenario's script emits, the delta
// stream a web client gets for it, and the snapshot that stream ends on.
type conformance struct {
	Scenario      string           `json:"scenario"`
	Provider      model.Provider   `json:"provider"`
	Prompt        string           `json:"prompt"`
	RuntimeEvents []session.Event  `json:"runtimeEvents"`
	Deltas        []session.Delta  `json:"deltas"`
	Snapshot      session.Snapshot `json:"snapshot"`
}

// stream numbers deltas as the chat loop does: one revision per event,
// stamped with its time, from a new chat's snapshot at seq 1. The prompt
// goes in after the provider's start events, as chatTurn adds it. Unlike
// the loop it never coalesces appends: that merge depends on wall time.
func stream(t *testing.T, provider model.Provider, prompt string, events []session.Event) ([]session.Delta, session.Snapshot) {
	t.Helper()
	var s *session.Snapshot
	var out []session.Delta
	commit := func(ds []session.Delta, stamp string) {
		rev := int64(1)
		if s != nil {
			rev = s.Revision + 1
		}
		for _, d := range ds {
			d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, "chat", int64(len(out)+1), rev, stamp
			if d.Snapshot != nil {
				snap := *d.Snapshot
				snap.Revision, snap.UpdatedAt = rev, stamp
				d.Snapshot = &snap
			}
			n, err := session.Apply(s, d.Seq-1, d)
			if err != nil {
				t.Fatalf("%s delta: %v", d.Type, err)
			}
			s, out = &n, append(out, d)
		}
	}
	stamp := events[0].CreatedAt
	commit([]session.Delta{{Type: session.DeltaSnapshot, Snapshot: &session.Snapshot{
		SessionID: "chat", PaneID: "chat", WorkspaceID: "tab", Provider: provider,
		SessionState: session.StateReady, PermissionMode: "default", InteractionMode: "build",
	}}}, stamp)
	for i, e := range events {
		if i == 2 && prompt != "" { // the fake emits session.resume-id and its ready state at Start
			commit([]session.Delta{{Type: session.DeltaItemAdd, Item: &session.Item{ID: "prompt", Kind: session.KindUserPrompt, Text: prompt, UpdatedAt: stamp, Attachments: []session.Attachment{}}}}, stamp)
			commit([]session.Delta{{Type: session.DeltaStatus, Status: "working", SessionState: session.StateRunning, Running: true, LastCompletedAt: s.LastCompletedAt}}, stamp)
		}
		commit(session.Reduce(*s, e), e.CreatedAt)
	}
	return out, *s
}

// encode writes c with one event or delta per line, so a diff shows which
// ones changed, and the snapshot indented.
func encode(c conformance) ([]byte, error) {
	var b bytes.Buffer
	head, err := json.Marshal(map[string]any{"scenario": c.Scenario, "provider": c.Provider, "prompt": c.Prompt})
	if err != nil {
		return nil, err
	}
	b.Write(head[:len(head)-1])
	for _, list := range []struct {
		key   string
		items []any
	}{{"runtimeEvents", toAny(c.RuntimeEvents)}, {"deltas", toAny(c.Deltas)}} {
		b.WriteString(",\n\"" + list.key + "\": [")
		for i, v := range list.items {
			line, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("\n  ")
			b.Write(line)
		}
		b.WriteString("\n]")
	}
	snap, err := json.MarshalIndent(c.Snapshot, "", "  ")
	if err != nil {
		return nil, err
	}
	b.WriteString(",\n\"snapshot\": ")
	b.Write(snap)
	b.WriteString("\n}\n")
	return b.Bytes(), nil
}

func toAny[T any](s []T) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// scenario runs the fake script name and builds its fixture.
func scenario(t *testing.T, name string) conformance {
	script, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	c := conformance{Scenario: name, Provider: script.Mode, Prompt: name}
	if c.Provider == "" {
		c.Provider = model.ProviderClaude
	}
	o := testOptions()
	o.ScriptName = name
	// aide's default runs 150 s; two keep the fixture small and still reach a tool.
	o.Stream = &StreamConfig{TokensPerSecond: 50, Seconds: 2, ToolEvery: 40, ToolMix: []string{"Bash", "Edit", "Read"}}
	c.RuntimeEvents = replayWith(t, name, c.Provider, o)
	if name == "fail-start" { // the chat loop's error when Start fails; no prompt goes out
		c.Prompt = ""
		c.RuntimeEvents = []session.Event{{Type: session.EventError, CreatedAt: session.Stamp(o.Clock.Now()), Message: "claude did not start: Fake managed provider failed to start"}}
	}
	c.Deltas, c.Snapshot = stream(t, c.Provider, c.Prompt, c.RuntimeEvents)
	return c
}

// TestConformance checks every fake scenario against its fixture in
// testdata/web-protocol, byte for byte. -update rewrites them.
func TestConformance(t *testing.T) {
	names := Names()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			b, err := encode(scenario(t, name))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(conformanceDir, name+".json")
			if *update {
				if err := os.WriteFile(path, b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v; run go test ./internal/session/fake -run TestConformance -update", err)
			}
			if !bytes.Equal(b, want) {
				t.Fatalf("%s is stale; run go test ./internal/session/fake -run TestConformance -update and review the diff", path)
			}
			// What a client does with the file: decode each delta and apply it.
			var f struct {
				Deltas   []json.RawMessage `json:"deltas"`
				Snapshot json.RawMessage   `json:"snapshot"`
			}
			if err := json.Unmarshal(want, &f); err != nil {
				t.Fatal(err)
			}
			var s *session.Snapshot
			for i, raw := range f.Deltas {
				var d session.Delta
				if err := json.Unmarshal(raw, &d); err != nil {
					t.Fatal(err)
				}
				n, err := session.Apply(s, int64(i), d)
				if err != nil {
					t.Fatalf("seq %d: %v", d.Seq, err)
				}
				s = &n
			}
			got, _ := json.Marshal(s)
			var a, w bytes.Buffer
			_ = json.Compact(&a, got)
			_ = json.Compact(&w, f.Snapshot)
			if a.String() != w.String() {
				t.Fatalf("applied deltas:\n%s\nfixture snapshot:\n%s", a.String(), w.String())
			}
		})
	}
	files, _ := filepath.Glob(filepath.Join(conformanceDir, "*.json"))
	for _, f := range files {
		if name := strings.TrimSuffix(filepath.Base(f), ".json"); !slices.Contains(names, name) {
			t.Errorf("%s has no fake script; delete it", f)
		}
	}
	// SHA256SUMS is what aide's vendor script fetches and checks the files against.
	var sums strings.Builder
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(conformanceDir, name+".json"))
		if err != nil {
			continue // its subtest failed
		}
		fmt.Fprintf(&sums, "%x  %s.json\n", sha256.Sum256(b), name)
	}
	path := filepath.Join(conformanceDir, "SHA256SUMS")
	if *update {
		if err := os.WriteFile(path, []byte(sums.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != sums.String() {
		t.Errorf("%s is stale; run go test ./internal/session/fake -run TestConformance -update", path)
	}
}
