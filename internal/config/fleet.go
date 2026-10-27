package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Fleet is [fleet]: lanes that map a task's size to a preset, and what
// pitwall does about workers that run long or keep failing CI.
type Fleet struct {
	Enabled  *bool  `toml:"enabled" doc:"Fleet mode: pitwall worktree new --lane starts a lane's preset, prepends the worker contract, nudges a worker past its timebox and escalates repeat red checks. Off by default"`
	Contract string `toml:"contract" doc:"A text file whose contents go before every brief pitwall worktree new --lane starts, such as how to commit and when to stop. Empty prepends nothing"`
	Nudge    string `toml:"nudge" doc:"What pitwall sends, once, to a worker past its lane's timebox with no pull request open. It types it at the worker's prompt, as pitwall send does"`
	Escalate string `toml:"escalate" enum:"parent,respawn,off" doc:"When CI fails again after auto_send_failures sent a worker its failures twice: parent tells the tab that started it, respawn closes the worker's agent and starts the big lane in the same worktree with the diff stat, the failure and the old agent's last screen as its brief, off does nothing"`
	Small    Lane   `toml:"small" doc:"One clear pull request, mechanical work included."`
	Normal   Lane   `toml:"normal" doc:"Normal work."`
	Big      Lane   `toml:"big" doc:"Whole features, investigations and risky changes."`
}

// Lane is [fleet.small], [fleet.normal] or [fleet.big].
type Lane struct {
	Preset  string   `toml:"preset" doc:"The [[agents.preset]] name this lane starts. Empty turns the lane off"`
	Timebox *float64 `toml:"timebox" min:"0" max:"1440" doc:"Minutes a worker on this lane may run with no pull request open before pitwall nudges it, once. Past twice this, pitwall tells the tab that started it. 0 turns it off"`
}

// LaneNames are the lanes in size order.
var LaneNames = []string{"small", "normal", "big"}

// LaneDescs say what each lane takes, in LaneNames order.
var LaneDescs = []string{"One clear pull request, mechanical work included.", "Normal work.", "Whole features, investigations and risky changes."}

// DefaultTimeboxes are the lanes' timebox defaults in minutes, in
// LaneNames order.
var DefaultTimeboxes = []float64{30, 60, 120}

// DefaultNudge is [fleet] nudge's default.
const DefaultNudge = "You are past this task's timebox and no pull request is open. Commit what works, open the pull request, and list what is left in it."

// FleetSettings is [fleet] resolved.
type FleetSettings struct {
	On       bool
	Contract string // the contract file's path, ~ expanded; "" for none
	Nudge    string
	Escalate string // parent, respawn or off
	Lanes    []LaneSettings
}

// LaneSettings is one lane resolved.
type LaneSettings struct {
	Name    string
	Preset  string // "" when the lane is off
	Timebox time.Duration
}

// Lane is the lane named name, if fleet mode is on and the lane has a
// preset.
func (f FleetSettings) Lane(name string) (LaneSettings, bool) {
	for _, l := range f.Lanes {
		if l.Name == name && f.On && l.Preset != "" {
			return l, true
		}
	}
	return LaneSettings{}, false
}

// ReadContract is the worker contract's text, "" without one.
func (f FleetSettings) ReadContract() (string, error) {
	if f.Contract == "" {
		return "", nil
	}
	b, err := os.ReadFile(f.Contract)
	if err != nil {
		return "", fmt.Errorf("[fleet] contract: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Brief is text with the worker contract before it.
func (f FleetSettings) Brief(text string) (string, error) {
	c, err := f.ReadContract()
	if err != nil || c == "" {
		return text, err
	}
	return c + "\n\n" + text, nil
}

// Timebox is the timebox of the lane named name, 0 for none.
func (f FleetSettings) Timebox(name string) time.Duration {
	for _, l := range f.Lanes {
		if l.Name == name {
			return l.Timebox
		}
	}
	return 0
}

func defaultFleet() Fleet {
	off := false
	f := Fleet{Enabled: &off, Nudge: DefaultNudge, Escalate: "parent"}
	for i, l := range []*Lane{&f.Small, &f.Normal, &f.Big} {
		tb := DefaultTimeboxes[i]
		l.Timebox = &tb
	}
	return f
}

// resolveFleet is c resolved against presets, the presets that loaded.
func resolveFleet(c Fleet, presets []AgentPreset) (FleetSettings, []issue) {
	f := FleetSettings{On: c.Enabled != nil && *c.Enabled, Nudge: strings.TrimSpace(c.Nudge), Escalate: c.Escalate}
	var issues []issue
	if f.Nudge == "" {
		f.Nudge = DefaultNudge
	}
	switch c.Escalate {
	case "":
		f.Escalate = "parent"
	case "parent", "respawn", "off":
	default:
		issues = append(issues, issue{"fleet.escalate", fmt.Sprintf("%q is not parent, respawn or off; using parent", c.Escalate)})
		f.Escalate = "parent"
	}
	if p := strings.TrimSpace(c.Contract); p != "" {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, rest)
		}
		f.Contract = p
	}
	for i, l := range []Lane{c.Small, c.Normal, c.Big} {
		name := LaneNames[i]
		ls := LaneSettings{Name: name, Preset: strings.TrimSpace(l.Preset), Timebox: time.Duration(DefaultTimeboxes[i] * float64(time.Minute))}
		if ls.Preset != "" && !slices.ContainsFunc(presets, func(p AgentPreset) bool { return p.Name == ls.Preset }) {
			issues = append(issues, issue{"fleet." + name + ".preset", fmt.Sprintf("no [[agents.preset]] named %q", ls.Preset)})
			ls.Preset = ""
		}
		switch v := l.Timebox; {
		case v == nil:
		case *v < 0 || *v > 1440:
			issues = append(issues, issue{"fleet." + name + ".timebox", fmt.Sprintf("%g is not a number of minutes from 0 to 1440", *v)})
		default:
			ls.Timebox = time.Duration(*v * float64(time.Minute))
		}
		f.Lanes = append(f.Lanes, ls)
	}
	return f, issues
}
