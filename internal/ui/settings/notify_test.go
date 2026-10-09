package settings

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
)

// The Notifications page writes muted_agents and quiet_hours so that the
// config reads them back, and keeps a bad quiet_hours out of the file.
func TestNotificationsPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	var p Page
	p.s.Path = path
	load := func() config.NotifySettings {
		s, probs := config.LoadFile(path)
		if len(probs) > 0 {
			t.Fatalf("%v", probs)
		}
		return s.Notifications
	}
	p.mute(nil, "codex", true)
	p.mute(load().MutedAgents, "terminal", true)
	if got := load().MutedAgents; !slices.Equal(got, []string{"codex", "terminal"}) {
		t.Fatalf("muted %q", got)
	}
	p.mute(load().MutedAgents, "codex", false)
	p.mute(load().MutedAgents, "terminal", false)
	if got := load().MutedAgents; len(got) != 0 {
		t.Fatalf("unmuted %q", got)
	}
	p.saveQuiet(" 22:30-07:00 ", "")
	if n := load(); n.QuietHours != "22:30-07:00" || p.nt.err != "" {
		t.Fatalf("quiet %+v %q", n, p.nt.err)
	}
	p.saveQuiet("late", "22:30-07:00")
	if n := load(); n.QuietHours != "22:30-07:00" || p.nt.err == "" {
		t.Fatalf("bad quiet saved: %+v", n)
	}
	p.saveQuiet("", "22:30-07:00")
	if n := load(); n.QuietHours != "" || p.nt.err != "" {
		t.Fatalf("quiet not cleared: %+v", n)
	}
}
