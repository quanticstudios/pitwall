package agent

import (
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// ScreenCheck records a pattern evaluated against the bottom screen rows.
type ScreenCheck struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Matched bool   `json:"matched"`
	Line    string `json:"line"`
}

// ScreenTrace holds the screen decision and the checks reached before it.
type ScreenTrace struct {
	Provider model.Provider   `json:"provider"`
	State    model.AgentState `json:"state"`
	Rule     string           `json:"rule"`
	Matched  *ScreenCheck     `json:"matched"`
	Checks   []ScreenCheck    `json:"checks"`
}

// ExplainScreen runs StateOf with evidence collection enabled.
func ExplainScreen(p model.Provider, g vt.Grid) ScreenTrace {
	t := ScreenTrace{Provider: p, Rule: "no-screen-match", Checks: []ScreenCheck{}}
	t.State = activePatterns.Load().stateOf(p, g, &t)
	return t
}

func (t *ScreenTrace) contains(kind, pattern, line, low string) bool {
	return t.check(kind+"/"+pattern, pattern, line, strings.Contains(low, pattern))
}

func (t *ScreenTrace) containsAny(kind string, patterns []string, line, low string) bool {
	for _, pattern := range patterns {
		if t.contains(kind, pattern, line, low) {
			return true
		}
	}
	return false
}

func (t *ScreenTrace) check(name, pattern, line string, matched bool) bool {
	if t == nil {
		return matched
	}
	c := ScreenCheck{Name: name, Pattern: pattern, Matched: matched}
	if matched {
		c.Line = line
	}
	for i, old := range t.Checks {
		if old.Name == name && old.Pattern == pattern {
			if matched && !old.Matched {
				t.Checks[i] = c
			}
			return matched
		}
	}
	t.Checks = append(t.Checks, c)
	return matched
}

func (t *ScreenTrace) decide(kind string) {
	if t == nil {
		return
	}
	for _, c := range t.Checks {
		if c.Matched && strings.HasPrefix(c.Name, kind+"/") {
			t.Rule, t.Matched = c.Name, &c
			return
		}
	}
}
