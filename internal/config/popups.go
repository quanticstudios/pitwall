package config

import (
	"fmt"
	"slices"
	"strings"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Popup is one [[popups]] entry: a command that opens in a popup over
// the shown tab on a key.
type Popup struct {
	Name    string   `toml:"name" doc:"Names the popup in problems, and is its title unless title is set"`
	Key     string   `toml:"key" doc:"The chord that opens it over the shown tab, such as \"Ctrl+Alt+G\". It must be free: no action and no other popup has it"`
	Command []string `toml:"command" doc:"The command and its arguments, such as [\"lazygit\"]. It runs in the tab's folder, and the popup closes when it exits"`
	Width   string   `toml:"width" doc:"A number of cells, or a percentage of the tab's area such as \"90%\". 80% by default"`
	Height  string   `toml:"height" doc:"As width. 70% by default"`
	Title   string   `toml:"title" doc:"Shown on the popup's border; name by default"`

	Chord Chord `toml:"-"` // Key parsed
}

// resolvePopups is c without the entries with problems, which it reports.
// A key must be free: no action and no other popup has it.
func resolvePopups(c []Popup, keys *Bindings) ([]Popup, []issue) {
	var out []Popup
	var issues []issue
	for _, p := range c {
		msg := ""
		switch {
		case strings.TrimSpace(p.Name) == "":
			msg = "a popup needs a name"
		case len(p.Command) == 0 || p.Command[0] == "":
			msg = fmt.Sprintf("popup %q needs a command", p.Name)
		case p.Key == "":
			msg = fmt.Sprintf("popup %q needs a key", p.Name)
		case slices.ContainsFunc(out, func(o Popup) bool { return o.Name == p.Name }):
			msg = fmt.Sprintf("a second popup is named %q", p.Name)
		}
		for _, size := range []string{p.Width, p.Height} {
			if _, _, err := model.ParsePopupSize(size); msg == "" && size != "" && err != nil {
				msg = fmt.Sprintf("popup %q: %v", p.Name, err)
			}
		}
		if msg == "" {
			c, err := ParseChord(p.Key)
			switch {
			case err != nil:
				msg = fmt.Sprintf("popup %q: %v", p.Name, err)
			case keys.Action(key.Event{Name: c.Name, Modifiers: c.Mods}) != "":
				msg = fmt.Sprintf("popup %q: %s is %s's key", p.Name, c, keys.Action(key.Event{Name: c.Name, Modifiers: c.Mods}))
			case slices.ContainsFunc(out, func(o Popup) bool { return o.Chord == c }):
				msg = fmt.Sprintf("popup %q: another popup has %s", p.Name, c)
			}
			p.Chord = c
		}
		if msg != "" {
			issues = append(issues, issue{"popups", msg})
			continue
		}
		out = append(out, p)
	}
	return out, issues
}

// PopupFor is the popup whose key e is, if any.
func (s Settings) PopupFor(e key.Event) (Popup, bool) {
	i := slices.IndexFunc(s.Popups, func(p Popup) bool { return p.Chord.Match(e) })
	if i < 0 {
		return Popup{}, false
	}
	return s.Popups[i], true
}
