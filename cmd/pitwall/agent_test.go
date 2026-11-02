package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestAgentExplainFile(t *testing.T) {
	// A file explanation must work without a daemon.
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	for _, name := range []string{"codex.txt", "codex-working.txt", "tuios-codex-working.txt", "screen.txt"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, []byte("\x1b[32mDo you want to proceed?\x1b[0m\r\nEsc to cancel\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"agent", "explain", "--file", path, "--json"}
			if name == "screen.txt" {
				args = append(args, "--provider", "codex")
			}
			code, out, stderr := cliOutput(args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
			var r proto.AgentExplanation
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatal(err)
			}
			if r.SchemaVersion != 1 || r.Provider != model.ProviderCodex || r.State != model.StatePendingApproval ||
				r.Rule != "form/esc to cancel" || r.Screen.Matched == nil || r.Screen.Matched.Line != "Esc to cancel" ||
				r.Process != nil || r.Hook != nil {
				t.Fatalf("got %+v", r)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &fields); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(fields))
			for k := range fields {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if want := []string{"hook", "pane", "process", "provider", "rule", "schema_version", "screen", "state"}; !reflect.DeepEqual(keys, want) {
				t.Fatalf("JSON fields = %v", keys)
			}
			args = slices.Delete(args, 4, 5) // --json
			code, out, stderr = cliOutput(args...)
			if code != 0 || stderr != "" || !strings.Contains(out, "state: pending-approval\n") ||
				!strings.Contains(out, `matched form/esc to cancel pattern="esc to cancel" line="Esc to cancel"`) {
				t.Fatalf("text %d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestAgentExplainErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "screen.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{}, {"unknown"}, {"explain"}, {"explain", "1", "2"},
		{"explain", "--file", path, "1"}, {"explain", "--file", path, "-s", "main"},
		{"explain", "--file", path}, {"explain", "--file", path, "--provider", "unknown"},
		{"explain", "--file", path, "--wat"}, {"explain", "--file", path + ".absent", "--provider", "codex"},
	} {
		code, _, stderr := cliOutput(append([]string{"agent"}, args...)...)
		if code != 1 || stderr == "" {
			t.Errorf("%v: code %d, stderr %q", args, code, stderr)
		}
	}
}
