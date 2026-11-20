package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The update button offers a restart while a local daemon is at an older
// Level, and its click asks before restarting.
func TestRestartOlderDaemon(t *testing.T) {
	b := &linked{FakeBackend: NewFakeBackend(), link: Link{Epoch: 1, Level: proto.Level - 1}}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	if got := u.updateLabel(); got != updated {
		t.Fatalf("label with an older daemon: %q", got)
	}
	u.sidebarEvent(&model.State{}, sidebar.RunUpdate{})
	if u.modal.kind != modalRestart {
		t.Fatal("the click did not ask")
	}
	for _, tc := range []struct {
		name string
		link Link
		host string
	}{
		{"current", Link{Level: proto.Level}, ""},
		{"down", Link{State: LinkDown, Level: proto.Level - 1}, ""},
		{"remote", Link{Level: proto.Level - 1}, "box"},
	} {
		b.link, Host = tc.link, tc.host
		if got := u.updateLabel(); got != "" {
			t.Errorf("%s: label %q", tc.name, got)
		}
	}
	Host = ""
}

// A daemon at this Level that runs an older release is behind too. A dev,
// dirty or describe build on either side compares by Level alone.
func TestDaemonBehind(t *testing.T) {
	b := &linked{FakeBackend: NewFakeBackend()}
	u := &ui{b: b}
	defer func(v string) { Version = v }(Version)
	for _, tc := range []struct {
		name, window string
		link         Link
		want         bool
	}{
		{"older build", "v0.1.1", Link{Level: proto.Level, Build: "v0.1.0-beta.18"}, true},
		{"same build", "v0.1.1", Link{Level: proto.Level, Build: "v0.1.1"}, false},
		{"newer build", "v0.1.1", Link{Level: proto.Level, Build: "v0.1.2"}, false},
		{"dev daemon", "v0.1.1", Link{Level: proto.Level, Build: "dev-abc123"}, false},
		{"dev window", "dev-abc123", Link{Level: proto.Level, Build: "v0.1.1"}, false},
		{"dirty daemon", "v0.1.2", Link{Level: proto.Level, Build: "v0.1.1-dirty"}, false},
		{"dirty window", "v0.1.2-dirty", Link{Level: proto.Level, Build: "v0.1.1"}, false},
		{"describe daemon", "v0.1.2", Link{Level: proto.Level, Build: "v0.1.1-3-g0123abc"}, false},
		{"describe window", "v0.1.2-3-g0123abc", Link{Level: proto.Level, Build: "v0.1.1"}, false},
		{"lower level", "v0.1.1", Link{Level: proto.Level - 1}, true},
		{"lower level, dev window", "dev-abc123", Link{Level: proto.Level - 1, Build: "v0.1.1"}, true},
	} {
		Version, b.link = tc.window, tc.link
		if got := u.daemonBehind(); got != tc.want {
			t.Errorf("%s: behind %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A window older than its daemon's release, as after an update moved the
// daemon onto the new binary, offers Restart to finish; the click reopens
// only the window, on its session and tab, and never restarts the daemon.
func TestRestartOlderWindow(t *testing.T) {
	defer func(v string) { Version, Relaunch = v, nil }(Version)
	Version = "v0.3.0"
	var to [2]string
	Relaunch = func(session, ws string) error { to = [2]string{session, ws}; return nil }
	b := &linked{FakeBackend: NewFakeBackend(), link: Link{Epoch: 1, Level: proto.Level, Build: "v0.3.1"}}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.nav.session, u.nav.workspace = "s1", "w1"
	if !u.windowBehind() || u.daemonBehind() || u.updateLabel() != updated {
		t.Fatalf("behind %v, daemon behind %v, label %q", u.windowBehind(), u.daemonBehind(), u.updateLabel())
	}
	u.sidebarEvent(&model.State{Sessions: []model.Session{{ID: "s1", Name: "main"}}}, sidebar.RunUpdate{})
	if !u.relaunched || to != [2]string{"main", "w1"} || u.modal.kind == modalRestart || b.restarts != 0 {
		t.Fatalf("relaunched %v on %v, modal %v, daemon restarts %d", u.relaunched, to, u.modal.kind, b.restarts)
	}
	for _, tc := range []struct {
		name, build, host string
		relaunch          bool
	}{
		{"same release", "v0.3.0", "", true},
		{"dev daemon", "dev-abc123", "", true},
		{"remote", "v0.3.1", "box", true},
		{"no relaunch", "v0.3.1", "", false},
	} {
		b.link.Build, Host = tc.build, tc.host
		if !tc.relaunch {
			Relaunch = nil
		}
		if u.windowBehind() {
			t.Errorf("%s: behind", tc.name)
		}
	}
	Host = ""
}
