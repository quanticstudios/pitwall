package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
)

// TestStatuslinePassthrough: the wrapped command gets Claude Code's JSON
// and its output, escapes, invalid UTF-8 and missing final newline
// included, and exit code come through unchanged, while the limits land
// in the state directory.
func TestStatuslinePassthrough(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	in := `{"model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":91,"resets_at":1800003600}}}`
	var out, errOut bytes.Buffer
	code := runStatusline([]string{`cat; printf '\033[1m it'"'"'s\377 \n\n  end'; exit 3`}, strings.NewReader(in), &out, &errOut)
	want := in + "\x1b[1m it's\xff \n\n  end"
	if code != 3 || out.String() != want || errOut.Len() != 0 {
		t.Fatalf("code %d, out %q, want %q, stderr %q", code, out.String(), want, errOut.String())
	}
	data, err := os.ReadFile(filepath.Join(state, "pitwall", flow.ClaudeLimitsFile))
	if err != nil {
		t.Fatal(err)
	}
	var l flow.Limit
	if json.Unmarshal(data, &l) != nil || len(l.Windows) != 1 || l.Windows[0].Used != 91 {
		t.Fatalf("saved %s", data)
	}

	out.Reset()
	if code := runStatusline(nil, strings.NewReader(in), &out, &errOut); code != 0 || out.Len() != 0 {
		t.Fatalf("no command: code %d, out %q", code, out.String())
	}
}

// TestHooksStatusline: install --statusline wraps the user's statusLine
// command and keeps its other fields, a second install changes nothing,
// a plain install leaves it alone, and uninstall puts the command back;
// without one, uninstall drops the statusLine it added.
func TestHooksStatusline(t *testing.T) {
	home := hooksHome(t)
	bin := testBin
	path := filepath.Join(home, ".claude", "settings.json")
	mine := `~/.claude/statusline.sh --say 'it'"'"'s'`
	writeHooksTestFile(t, path, `{"statusLine":{"type":"command","command":`+quoteJSON(mine)+`,"padding":2}}`)
	statusLine := func() map[string]any {
		t.Helper()
		var v struct{ StatusLine map[string]any }
		if err := json.Unmarshal(readHooksTestFile(t, path), &v); err != nil {
			t.Fatal(err)
		}
		return v.StatusLine
	}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := runHooks(args, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	run("install")
	if got := statusLine()["command"]; got != mine {
		t.Fatalf("a plain install changed the status line: %v", got)
	}
	if out := run("install", "--statusline", "--dry-run"); !strings.Contains(out, "wrapped statusLine: "+mine) {
		t.Fatalf("dry run: %s", out)
	}
	run("install", "--statusline")
	sl := statusLine()
	if sl["command"] != agent.ClaudeStatusline(bin, mine) || sl["padding"] != 2.0 || sl["type"] != "command" {
		t.Fatalf("installed %v", sl)
	}
	if out := run("install", "--statusline"); strings.Contains(out, "statusLine") {
		t.Fatalf("second install: %s", out)
	}
	run("uninstall")
	if sl := statusLine(); sl["command"] != mine || sl["padding"] != 2.0 {
		t.Fatalf("uninstalled %v", sl)
	}

	writeHooksTestFile(t, path, `{"model":"keep"}`)
	run("install", "--statusline")
	if sl := statusLine(); sl["command"] != agent.ClaudeStatusline(bin, "") {
		t.Fatalf("installed %v", sl)
	}
	run("uninstall")
	if sl := statusLine(); sl != nil {
		t.Fatalf("uninstall kept %v", sl)
	}
}

func quoteJSON(s string) string { b, _ := json.Marshal(s); return string(b) }
