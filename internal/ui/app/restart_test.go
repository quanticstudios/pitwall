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
