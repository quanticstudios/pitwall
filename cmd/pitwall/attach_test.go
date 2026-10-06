package main

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// pitwall attach opens a window on the tab's session only when no window
// shows it; one that does got the FocusSession and raises itself.
func TestCLIAttach(t *testing.T) {
	for _, shown := range []bool{false, true} {
		t.Run(fmt.Sprint(shown), func(t *testing.T) {
			after := cliState()
			if shown {
				after.Sessions[0].Windows = 1
			}
			fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.FocusSession{WorkspaceID: "b"}, state: after})
			t.Setenv("PITWALL_PANE", "pb")
			previous := launchGUI
			var launched []string
			launchGUI = func(session, id string) error { launched = append(launched, session+"/"+id); return nil }
			t.Cleanup(func() { launchGUI = previous })
			args := []string{"attach"}
			if shown {
				args = append(args, "alpi")
			}
			if code, out, stderr := cliOutput(args...); code != 0 || out != "" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
			if shown && len(launched) != 0 {
				t.Fatal("launched a second window on a shown session")
			}
			if !shown && (len(launched) != 1 || launched[0] != "main/b") {
				t.Fatalf("launched: %v", launched)
			}
		})
	}
}

func TestCLIAttachErrors(t *testing.T) {
	for _, daemonError := range []bool{false, true} {
		t.Run(fmt.Sprint(daemonError), func(t *testing.T) {
			exchange := cliExchange{request: proto.FocusSession{WorkspaceID: "a"}, state: cliState()}
			if daemonError {
				exchange.error = "cannot focus"
			}
			fakeCLI(t, cliExchange{state: cliState()}, exchange)
			previous := launchGUI
			launched := false
			launchGUI = func(string, string) error { launched = true; return errors.New("cannot launch") }
			t.Cleanup(func() { launchGUI = previous })
			code, out, stderr := cliOutput("attach", "1")
			want := "pitwall: cannot launch\n"
			if daemonError {
				want = "pitwall: cannot focus\n"
			}
			if code != 1 || out != "" || stderr != want || launched == daemonError {
				t.Fatalf("%d: %s %s; launched %v", code, out, stderr, launched)
			}
		})
	}
}

// A new window opens on the named session, raising its window if it has
// one; without a name, on the most recent session no window shows.
func TestGUITarget(t *testing.T) {
	now := time.Now()
	st := model.State{Sessions: []model.Session{
		{ID: "a", Name: "swift-otter", UsedAt: now.Add(-time.Hour)},
		{ID: "b", Name: "calm-heron", UsedAt: now, Windows: 1},
	}}
	for _, tc := range []struct {
		name, want string
		raise      bool
	}{{"", "a", false}, {"swift-otter", "a", false}, {"calm-heron", "b", true}, {"absent", "", false}} {
		if s, raise := guiTarget(st, tc.name); s.ID != tc.want || raise != tc.raise {
			t.Errorf("%q: %s raise %v, want %s raise %v", tc.name, s.ID, raise, tc.want, tc.raise)
		}
	}
}

func TestBackendFocus(t *testing.T) {
	t.Setenv("PITWALL_ATTACH", "startup")
	path := filepath.Join(t.TempDir(), "gui.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	served := make(chan error, 1)
	wanted := []proto.FocusSession{{WorkspaceID: "first", TabID: "tab"}, {WorkspaceID: "second"}}
	go func() {
		served <- func() error {
			nc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer nc.Close()
			nc.SetDeadline(time.Now().Add(5 * time.Second))
			c := proto.NewConn(nc)
			for _, focus := range wanted {
				if err := c.Send(focus); err != nil {
					return err
				}
			}
			return c.Send(proto.StateMsg{State: model.State{Version: 99}})
		}()
	}()
	c, err := proto.Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := newBackend(c, "first-session")
	if got := <-b.Focus(); got != (proto.FocusSession{WorkspaceID: "startup", SessionID: "first-session"}) {
		t.Fatalf("startup: %+v", got)
	}
	select {
	case got := <-b.Focus():
		t.Fatalf("startup repeats: %+v", got)
	default:
	}
	done := make(chan struct{})
	go func() { b.recvLoop(); close(done) }()
	// The newest request wins, so "first" may be dropped when "second" lands
	// before it is read; "second" always arrives, and nothing older after it.
	last := wanted[len(wanted)-1]
	for got := (proto.FocusSession{}); got != last; {
		select {
		case got = <-b.Focus():
			if got != wanted[0] && got != last {
				t.Fatalf("focus: %+v, want %+v or %+v", got, wanted[0], last)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("focus did not arrive")
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not stop")
	}
	select {
	case got := <-b.Focus():
		t.Fatalf("an older focus after the newest: %+v", got)
	default:
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if b.State().Version != 99 {
		t.Fatal("focus prevented state updates")
	}
	t.Setenv("PITWALL_ATTACH", "")
	select {
	case got := <-newBackend(nil, "").Focus():
		t.Fatalf("unexpected startup: %+v", got)
	default:
	}
}
