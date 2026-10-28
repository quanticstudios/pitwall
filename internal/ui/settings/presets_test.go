package settings

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestPresetEditor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	src := `[[agents.preset]]
name = "Quick"
agent = "codex"
args = ["-m", "gpt-x", "-c", "model_reasoning_effort=low", "--search"]
env = ["CODEX_HOME=/tmp/x"]

[fleet.small]
preset = "Quick"

[fleet.big]
preset = "Quick"
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var p Page
	p.Show(path)
	load := func() {
		t.Helper()
		var probs []config.Problem
		if p.s, probs = config.LoadFile(path); len(probs) > 0 {
			t.Fatal(probs)
		}
	}
	load()
	if s := presetSummary(p.s.Presets[0]); s != "Codex, gpt-x, low, --search" {
		t.Errorf("summary %q", s)
	}

	// Rename it and change the effort: the lanes follow, env stays.
	p.editPreset(&p.s.Presets[0])
	if p.pr.model.Text() != "gpt-x" || p.pr.effort != "low" || p.pr.extra.Text() != "--search" {
		t.Fatalf("editor %q %q %q", p.pr.model.Text(), p.pr.effort, p.pr.extra.Text())
	}
	p.pr.name.SetText("Fast")
	p.pr.effort = "high"
	p.pr.extra.SetText(`--search --note 'two words'`)
	p.savePreset()
	load()
	want := config.AgentPreset{Name: "Fast", Agent: "codex", Env: []string{"CODEX_HOME=/tmp/x"},
		Args: []string{"-m", "gpt-x", "-c", "model_reasoning_effort=high", "--search", "--note", "two words"}}
	if got := p.s.Presets; p.pr.open || len(got) != 1 || got[0].Name != want.Name || !slices.Equal(got[0].Args, want.Args) || !slices.Equal(got[0].Env, want.Env) {
		t.Fatalf("after save %+v (open %v, %q)", got, p.pr.open, p.pr.err)
	}
	if p.s.Fleet.Lanes[0].Preset != "Fast" || p.s.Fleet.Lanes[2].Preset != "Fast" {
		t.Fatalf("lanes %+v", p.s.Fleet.Lanes)
	}

	// A bad quote does not save.
	p.editPreset(&p.s.Presets[0])
	p.pr.extra.SetText(`"open`)
	p.savePreset()
	if !p.pr.open || p.pr.err == "" {
		t.Fatal("an open quote saved")
	}

	// Delete it: both lanes turn off, and the page says so.
	p.deletePreset()
	load()
	if len(p.s.Presets) != 0 || p.s.Fleet.Lanes[0].Preset != "" || p.s.Fleet.Lanes[2].Preset != "" {
		t.Fatalf("after delete %+v %+v", p.s.Presets, p.s.Fleet.Lanes)
	}
	if !strings.Contains(p.pr.note, "The small and big lanes are off now.") {
		t.Errorf("note %q", p.pr.note)
	}
}

func TestPresetEditorSurvivesSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[agents.preset]]\nname = \"Quick\"\nagent = \"claude\"\nargs = [\"--model\", \"opus\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var p Page
	p.th = theme.Dark()
	p.Show(path)
	p.s, _ = config.LoadFile(path)
	p.editPreset(&p.s.Presets[0])
	// "zzz" matches no row, yet the open editor keeps all of its rows, the buttons' included.
	var kept []section
	for i := range categories {
		kept = append(kept, p.searchSections(i, "zzz")...)
	}
	full := 0
	for i := range categories {
		for _, sec := range p.sections(i) {
			if sec.anchor {
				full += len(sec.rows)
			}
		}
	}
	got := 0
	for _, sec := range kept {
		got += len(sec.rows)
	}
	if full == 0 || got != full {
		t.Fatalf("search kept %d of the editor's %d rows", got, full)
	}
}
