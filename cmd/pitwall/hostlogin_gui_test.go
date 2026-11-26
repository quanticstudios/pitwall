//go:build unix && !nogui

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// A window whose host waits on a Tailscale check says so with the link,
// tries once, and tries again only when the shared connection opens, as
// pitwall host login leaves it.
func TestWindowWaitsForHostLogin(t *testing.T) {
	shortStart(t)
	prevWait, prevPoll, prevMin := loginWait, loginPoll, retryMin
	t.Cleanup(func() { loginWait, loginPoll, retryMin = prevWait, prevPoll, prevMin })
	loginWait, loginPoll, retryMin = time.Hour, 10*time.Millisecond, time.Millisecond
	// ssh.log counts the tries; "login" stands for the shared connection.
	r, dir := scriptHost(t, `case "$*" in *"-O check"*) test -e "$dir/login"; exit;; esac
echo try >>"$dir/ssh.log"
`+checkOutput+`
exec sleep 60`)
	tries := func() int {
		data, _ := os.ReadFile(filepath.Join(dir, "ssh.log"))
		return strings.Count(string(data), "try")
	}
	local, _ := localBackend(t, hostState("ls", "lw", "lp"))
	var over atomic.Bool
	m := newMulti(local, []hostDial{{name: "box", dial: func(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
		if over.Load() {
			return nil, proto.StateMsg{}, incompatible{proto.Version - 1}
		}
		return r.dial(session, restart)
	}}})
	waitFor(t, "box asking", func() bool { return m.Hosts()[1].Login != nil })
	h := m.Hosts()[1]
	if h.Up || h.Login.Reason != "Tailscale SSH wants an approval" || h.Login.Command != "pitwall host login -- 'box'" ||
		h.Login.URL != "https://login.tailscale.com/a/1f2e3d4c5b6a7" || h.Note != "Tailscale SSH wants an approval. Run pitwall host login -- 'box'" ||
		time.Until(h.Retry) < 59*time.Minute {
		t.Fatalf("%+v %+v", h, h.Login)
	}
	time.Sleep(200 * time.Millisecond)
	if n := tries(); n != 1 {
		t.Fatalf("%d tries into the prompt", n)
	}
	os.WriteFile(filepath.Join(dir, "login"), nil, 0o600)
	waitFor(t, "a try after login", func() bool { return tries() == 2 })
	// A connection that is open while tries still fail retries once, not
	// on every poll.
	waitFor(t, "the second failure", func() bool { h := m.Hosts()[1]; return !h.Retry.IsZero() })
	time.Sleep(200 * time.Millisecond)
	if n := tries(); n != 2 {
		t.Fatalf("%d tries with the connection open", n)
	}
	m.Reconnect() // Retry now
	waitFor(t, "a try on Retry now", func() bool { return tries() == 3 })
	waitFor(t, "the third failure", func() bool { return !m.Hosts()[1].Retry.IsZero() })
	if l := m.Link(); l.State != app.LinkUp {
		t.Fatalf("link %+v covers the window", l)
	}
	// why: the dial loop must end before the cleanups restore what it reads.
	over.Store(true)
	m.Reconnect()
	waitFor(t, "the loop's end", func() bool { return m.hosts[1].b.Link().State == app.LinkRestart })
	if h := m.Hosts()[1]; h.Login != nil {
		t.Fatalf("a daemon of another version still asks to log in: %+v", h.Login)
	}
}
