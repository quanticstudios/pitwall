package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestDecisions(t *testing.T) {
	dir := t.TempDir()
	s, probs := LoadFile(write(t, dir, "config.toml", ""))
	d := s.Decisions
	if len(probs) > 0 || d.On() || d.Approvals != ModeSuggest || !d.Triage || d.Agents || d.TurnCheck ||
		d.Timeout != 1500*time.Millisecond || d.Model != "jev-latest" {
		t.Fatalf("defaults: %+v %v", d, msgs(probs))
	}
	if !slices.Contains(d.Programs, "gemini") || !slices.Contains(d.Programs, "crush") {
		t.Errorf("programs = %v", d.Programs)
	}

	s, probs = LoadFile(write(t, dir, "config.toml", `[decisions]
provider = "jev"
timeout = 0.8
[decisions.approvals]
mode = "off"
never_allow = ["terraform apply"]
[decisions.triage]
enabled = false
[decisions.agents]
enabled = true
programs = ["mycli"]
[decisions.turn_check]
enabled = true
threshold = 0.9
`))
	d = s.Decisions
	if len(probs) > 0 || !d.On() || d.Approvals != ModeOff || d.Triage ||
		!d.Agents || !d.TurnCheck || d.TurnThreshold != 0.9 || d.Timeout != 800*time.Millisecond ||
		!slices.Contains(d.Programs, "mycli") || !slices.Equal(d.NeverAllow, []string{"terraform apply"}) {
		t.Fatalf("set: %+v %v", d, msgs(probs))
	}

	s, probs = LoadFile(write(t, dir, "config.toml", `[decisions]
provider = "jevv"
[decisions.approvals]
mode = "yolo"
`))
	got := msgs(probs)
	for _, want := range []string{`decisions.provider: "jevv" is not jev, command or "" (did you mean "jev"?)`, `decisions.approvals.mode: "yolo" is not off or suggest`} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %q lack %q", got, want)
		}
	}
	if d := s.Decisions; d.On() || d.Approvals != ModeSuggest {
		t.Errorf("bad values should keep defaults: %+v", d)
	}

	// A config from when automatic approval existed still loads: auto
	// becomes suggest, and every removed key gets a note, not a problem.
	s, probs = LoadFile(write(t, dir, "config.toml", `[decisions]
provider = "jev"
[decisions.approvals]
mode = "auto"
allow_above = 0.99
deny_above = 0.9
allow_programs = ["just"]
`))
	if len(probs) > 0 || s.Decisions.Approvals != ModeSuggest {
		t.Errorf("old auto config: %+v %v", s.Decisions, msgs(probs))
	}
	notes := msgs(s.Notes)
	for _, want := range []string{
		`config.toml:4: decisions.approvals.mode: "auto" is no longer supported`,
		"config.toml:5: decisions.approvals.allow_above: no longer supported",
		"config.toml:6: decisions.approvals.deny_above: no longer supported",
		"config.toml:7: decisions.approvals.allow_programs: no longer supported",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes %q lack %q", notes, want)
		}
	}
	s, probs = LoadFile(write(t, dir, "config.toml", "[decisions]\nprovider = \"command\"\n"))
	if s.Decisions.On() || !strings.Contains(msgs(probs), "needs a command") {
		t.Errorf("command without argv: %+v %v", s.Decisions, msgs(probs))
	}
	s, _ = LoadFile(write(t, dir, "config.toml", "[decisions]\nprovider = \"command\"\ncommand = [\"my-classifier\", \"--fast\"]\n"))
	if s.Decisions.Provider != "command" || !slices.Equal(s.Decisions.Command, []string{"my-classifier", "--fast"}) {
		t.Errorf("command: %+v", s.Decisions)
	}

	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	for doc, valid := range map[string]bool{
		"[decisions]\nprovider = \"jev\"\n":                         true,
		"[decisions]\nprovider = \"openai\"\n":                      false,
		"[decisions.approvals]\nmode = \"suggest\"\n":               true,
		"[decisions.approvals]\nmode = \"auto\"\n":                  false,
		"[decisions.approvals]\nmode = \"always\"\n":                false,
		"[decisions.approvals]\nallow_above = 0.5\n":                true, // deprecated, still valid
		"[decisions.agents]\nprograms = [\"x\"]\nthreshold = 0.7\n": true,
		"[decisions.triage]\nthreshold = 0.7\n":                     false,
	} {
		var m map[string]any
		if _, err := toml.Decode(doc, &m); err != nil {
			t.Fatal(err)
		}
		if errs := validate(root, root, m, ""); valid != (len(errs) == 0) {
			t.Errorf("schema on %q: valid=%v, errors %v", doc, valid, errs)
		}
	}
}
