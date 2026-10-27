package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// [fleet] resolves its lanes against the presets, reports a lane naming
// none and a bad escalate, and puts the contract before a brief.
func TestFleet(t *testing.T) {
	dir := t.TempDir()
	contract := filepath.Join(dir, "contract.md")
	os.WriteFile(contract, []byte("Commit on the branch.\n"), 0o644)
	p := write(t, dir, "config.toml", `[[agents.preset]]
name = "Quick"
agent = "claude"

[fleet]
enabled = true
contract = "`+filepath.ToSlash(contract)+`"
escalate = "loud"

[fleet.small]
preset = "Quick"
timebox = 15

[fleet.big]
preset = "Missing"
timebox = 2000
`)
	s, probs := LoadFile(p)
	f := s.Fleet
	if !f.On || f.Escalate != "parent" || f.Nudge != DefaultNudge {
		t.Errorf("fleet %+v", f)
	}
	if l, ok := f.Lane("small"); !ok || l.Preset != "Quick" || l.Timebox != 15*time.Minute {
		t.Errorf("small %+v %v", l, ok)
	}
	if _, ok := f.Lane("big"); ok || f.Timebox("big") != 2*time.Hour || f.Timebox("normal") != time.Hour {
		t.Errorf("big should be off at its default timebox: %+v", f.Lanes)
	}
	var got []string
	for _, pr := range probs {
		got = append(got, pr.Msg)
	}
	for _, want := range []string{`fleet.escalate: "loud" is not parent`, `fleet.big.preset: no [[agents.preset]] named "Missing"`, "fleet.big.timebox: 2000"} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("problems %q lack %q", got, want)
		}
	}
	if b, err := f.Brief("Fix the login."); err != nil || b != "Commit on the branch.\n\nFix the login." {
		t.Errorf("brief %q %v", b, err)
	}
	f.Contract = ""
	if b, _ := f.Brief("Fix the login."); b != "Fix the login." {
		t.Errorf("brief without a contract %q", b)
	}
	f.On = false
	if _, ok := f.Lane("small"); ok {
		t.Error("a lane with fleet mode off")
	}
}
