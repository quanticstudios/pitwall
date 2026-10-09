package config

import (
	"fmt"
	"strings"
	"time"
)

// Notify is [notifications]: which desktop notifications pitwall
// sends, and how.
type Notify struct {
	Sound       string   `toml:"sound" doc:"Play a sound with each notification: \"bell\", a sound file's path, or \"\" for none. Played with paplay, pw-play or afplay, whichever is installed."`
	Approval    *bool    `toml:"approval" doc:"Notify when an agent asks permission or has a plan to approve."`
	Input       *bool    `toml:"input" doc:"Notify when an agent asks a question, or a terminal asks for you (pitwall notify, a bell, OSC 9)."`
	Done        *bool    `toml:"done" doc:"Notify when an agent finishes a turn."`
	Failed      *bool    `toml:"failed" doc:"Notify when an agent's turn fails."`
	MutedAgents []string `toml:"muted_agents" doc:"Agents that never notify: claude, codex, pi, gemini, opencode, or terminal for pitwall notify and bells."`
	QuietHours  string   `toml:"quiet_hours" doc:"\"22:00-08:00\": between these times nothing plays a sound and only approvals, errors and what triage rates now notify. \"\" for none."`
}

// NotifySettings is [notifications] with every value filled in.
type NotifySettings struct {
	Sound                         string // "" for none
	Approval, Input, Done, Failed bool
	MutedAgents                   []string
	QuietHours                    string // as written, "" for none
	// QuietFrom and QuietTo are quiet_hours in minutes after midnight;
	// equal when there are none.
	QuietFrom, QuietTo int
}

func defaultNotifications() Notify {
	on := true
	return Notify{Approval: &on, Input: &on, Done: &on, Failed: &on, MutedAgents: []string{}}
}

// DefaultNotifySettings is [notifications] unset: every state notifies,
// with no sound.
func DefaultNotifySettings() NotifySettings {
	n, _ := resolveNotifications(Notify{})
	return n
}

// resolveNotifications fills in defaults. A quiet_hours that is not
// "HH:MM-HH:MM" is a problem, and means none.
func resolveNotifications(c Notify) (NotifySettings, []issue) {
	on := func(b *bool) bool { return b == nil || *b }
	n := NotifySettings{Sound: c.Sound, Approval: on(c.Approval), Input: on(c.Input), Done: on(c.Done), Failed: on(c.Failed), MutedAgents: c.MutedAgents}
	if c.QuietHours == "" {
		return n, nil
	}
	from, to, err := ParseQuietHours(c.QuietHours)
	if err != nil {
		return n, []issue{{"notifications.quiet_hours", err.Error() + "; no quiet hours"}}
	}
	n.QuietHours, n.QuietFrom, n.QuietTo = c.QuietHours, from, to
	return n, nil
}

// ParseQuietHours reads "22:00-08:00" as minutes after midnight. The span
// may cross midnight.
func ParseQuietHours(s string) (from, to int, err error) {
	var fh, fm, th, tm int
	var rest string
	if n, _ := fmt.Sscanf(strings.TrimSpace(s)+"|", "%d:%d-%d:%d%s", &fh, &fm, &th, &tm, &rest); n != 5 || rest != "|" ||
		fh > 23 || th > 23 || fm > 59 || tm > 59 || fh < 0 || th < 0 || fm < 0 || tm < 0 {
		return 0, 0, fmt.Errorf("%q is not HH:MM-HH:MM", s)
	}
	return fh*60 + fm, th*60 + tm, nil
}

// Quiet reports whether t falls in the quiet hours.
func (n NotifySettings) Quiet(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if n.QuietFrom <= n.QuietTo {
		return m >= n.QuietFrom && m < n.QuietTo
	}
	return m >= n.QuietFrom || m < n.QuietTo // across midnight
}
