package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// The picker opens on the current tab's machine, and on this machine when
// that one is down; a down host's row is not pickable and says why.
func TestPickStart(t *testing.T) {
	now := time.Now()
	hosts := []sidebar.HostInfo{
		{Up: true},
		{Name: "box", Up: true, SSH: "me@box", Latency: 38 * time.Millisecond},
		{Name: "gpu", Note: "cannot reach gpu over ssh: connection timed out. Trying again in 4s.", Retry: now.Add(4 * time.Second)},
	}
	items := pickItems(hosts, now)
	if got := pickStart(items, "box"); got != 1 {
		t.Fatalf("box tab: row %d", got)
	}
	if got := pickStart(items, ""); got != 0 {
		t.Fatalf("local tab: row %d", got)
	}
	if got := pickStart(items, "gpu"); got != 0 {
		t.Fatalf("down host's tab: row %d", got)
	}
	if !items[2].off || items[2].sub != "offline: cannot reach gpu over ssh: connection timed out, retry in 4s" {
		t.Fatalf("down row %+v", items[2])
	}
	if items[1].right != "38 ms" || items[1].sub != "~ on me@box" {
		t.Fatalf("box row %+v", items[1])
	}
}

// A paste asks first only with several lines, into another host's pane,
// soon after focus came there from another host.
func TestPasteAsks(t *testing.T) {
	now := time.Now()
	moved := hostMove{pane: "rp", at: now.Add(-2 * time.Second)}
	for _, c := range []struct {
		text, host, pane string
		moved            hostMove
		want             bool
	}{
		{"rm -rf build\nmake\n", "box", "rp", moved, true},
		{"one line\n", "box", "rp", moved, false},
		{"a\nb", "", "rp", moved, false},                                              // this machine's pane
		{"a\nb", "box", "other", moved, false},                                        // focus came to another pane
		{"a\nb", "box", "rp", hostMove{pane: "rp", at: now.Add(-time.Minute)}, false}, // long since
	} {
		if got := pasteAsks(c.text, c.host, c.pane, c.moved, now); got != c.want {
			t.Errorf("pasteAsks(%q, %q, %q) = %v", c.text, c.host, c.pane, got)
		}
	}
}

// A host tag's details: folder and latency while up; the error without
// its retry tail, the next try and the screen's time while down.
func TestTagRows(t *testing.T) {
	now := time.Date(2026, 11, 8, 14, 5, 0, 0, time.Local)
	up := sidebar.HostInfo{Name: "box", SSH: "me@box", Up: true, Latency: 38 * time.Millisecond}
	if got := tagRows(up, "~/evals", false, now); len(got) != 3 || got[1][1] != "~/evals" || got[2][1] != "38 ms" {
		t.Fatalf("up %v", got)
	}
	down := sidebar.HostInfo{Name: "box", SSH: "me@box", Note: "cannot reach box over ssh: connection timed out. Trying again in 30s.",
		Retry: now.Add(24 * time.Second), Since: now.Add(-3 * time.Minute)}
	got := tagRows(down, "~/evals", true, now)
	if len(got) != 4 || got[1][1] != "cannot reach box over ssh: connection timed out" || got[2][1] != "in 24s" || got[3][1] != "from 14:02" {
		t.Fatalf("down %v", got)
	}
}

// sharedFake is a window whose first tab is a shared session's tab on box.
type sharedFake struct {
	*FakeBackend
	tab string
}

func (sharedFake) SessionHost(string) string { return "" }
func (f sharedFake) HostOf(id string) string {
	if id == f.tab {
		return "box"
	}
	return ""
}
func (sharedFake) Hosts() []sidebar.HostInfo {
	return []sidebar.HostInfo{{Up: true}, {Name: "box"}}
}

// The board learns a shared session's remote tab's host by its tab id.
func TestBoardHostsShared(t *testing.T) {
	fb := NewFakeBackend()
	st := fb.State()
	f := sharedFake{fb, st.Workspaces[0].ID}
	u := &ui{b: f}
	got := u.boardHosts(&st)
	if len(got) != 1 || got[f.tab] != "box" {
		t.Fatalf("board hosts %v", got)
	}
}

// A tab a tab opens for itself, as Create PR, Merge PR and Rerun checks
// do, opens on that tab's host, even for an ungrouped tab of a shared
// session, whose request names no id of that host.
func TestFromTabOnItsHost(t *testing.T) {
	fb := NewFakeBackend()
	st := fb.State()
	f := sharedFake{fb, st.Workspaces[0].ID}
	u := &ui{b: f}
	ns := proto.NewSession{SessionID: st.Workspaces[0].SessionID, Loose: true, Cmd: []string{"gh", "pr", "create"}}
	u.send(fromTab{f.tab, ns})
	u.send(fromTab{st.Workspaces[1].ID, ns})
	sent := fb.Sent()
	if len(sent) != 2 || !reflect.DeepEqual(sent[0], NewTabOn{Host: "box", Tab: ns}) || !reflect.DeepEqual(sent[1], ns) {
		t.Fatalf("sent %#v", sent)
	}
}
