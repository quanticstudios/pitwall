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
		d.AllowAbove != 0.95 || d.DenyAbove != 0.95 || d.Timeout != 1500*time.Millisecond || d.Model != "jev-latest" {
		t.Fatalf("defaults: %+v %v", d, msgs(probs))
	}
	if !slices.Contains(d.Programs, "gemini") || !slices.Contains(d.Programs, "crush") {
		t.Errorf("programs = %v", d.Programs)
	}

	s, probs = LoadFile(write(t, dir, "config.toml", `[decisions]
provider = "jev"
timeout = 0.8
[decisions.approvals]
mode = "auto"
allow_above = 0.99
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
	if len(probs) > 0 || !d.On() || d.Approvals != ModeAuto || d.AllowAbove != 0.99 || d.DenyAbove != 0.95 || d.Triage ||
		!d.Agents || !d.TurnCheck || d.TurnThreshold != 0.9 || d.Timeout != 800*time.Millisecond ||
		!slices.Contains(d.Programs, "mycli") || !slices.Equal(d.NeverAllow, []string{"terraform apply"}) {
		t.Fatalf("set: %+v %v", d, msgs(probs))
	}

	s, probs = LoadFile(write(t, dir, "config.toml", `[decisions]
provider = "jevv"
[decisions.approvals]
mode = "yolo"
allow_above = 0.5
`))
	got := msgs(probs)
	for _, want := range []string{`decisions.provider: "jevv" is not jev, command or "" (did you mean "jev"?)`, "decisions.approvals.mode", "allow_above: 0.5 is outside 0.8-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %q lack %q", got, want)
		}
	}
	if d := s.Decisions; d.On() || d.Approvals != ModeSuggest || d.AllowAbove != 0.95 {
		t.Errorf("bad values should keep defaults: %+v", d)
	}

	s, probs = LoadFile(write(t, dir, "config.toml", "[decisions.approvals]\nallow_programs = [\"just\", \"./run\", \"/usr/bin/x\"]\n"))
	if !slices.Equal(s.Decisions.AllowPrograms, []string{"just"}) || !strings.Contains(msgs(probs), `"./run" is not a bare program name`) {
		t.Errorf("allow_programs: %v %v", s.Decisions.AllowPrograms, msgs(probs))
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
		"[decisions.approvals]\nmode = \"auto\"\n":                  true,
		"[decisions.approvals]\nmode = \"always\"\n":                false,
		"[decisions.approvals]\nallow_above = 0.5\n":                false,
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
