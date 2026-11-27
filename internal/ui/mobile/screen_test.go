package mobile

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/remote"
)

// screenAPI records the Screen view's reads, keys and stops.
type screenAPI struct {
	chatAPI
	reads       int
	keys, stops []string
	err         error
}

func (f *screenAPI) Screen(ctx context.Context, pane string) (string, error) {
	f.reads++
	return "❯ 1. Yes", f.err
}

func (f *screenAPI) Key(ctx context.Context, pane, key string) error {
	f.keys = append(f.keys, pane+" "+key)
	return f.err
}

func (f *screenAPI) Stop(ctx context.Context, pane string) error {
	f.stops = append(f.stops, pane)
	return f.err
}

func screenEnv(api chatAPI) chatEnv {
	return chatEnv{Online: true, Open: true, Tab: remote.Tab{Item: remote.Item{Pane: "p1", State: "working"}},
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			done(call(context.Background(), api))
		}}
}

// The view reads the screen once a second while it shows, at once after a
// key settles, and never while closed or offline.
func TestScreenReads(t *testing.T) {
	api := &screenAPI{}
	c := newChat("p1")
	env := screenEnv(api)
	t0 := time.Unix(1000, 0)
	frame := func(at time.Duration) {
		c.readScreen(layout.Context{Ops: new(op.Ops), Now: t0.Add(at)}, env)
	}
	frame(0)
	if api.reads != 0 {
		t.Fatal("read while closed")
	}
	c.scr.show()
	frame(0)
	frame(500 * time.Millisecond)
	if api.reads != 1 || c.scr.text != "❯ 1. Yes" || !c.scr.read {
		t.Fatalf("reads %d, text %q", api.reads, c.scr.text)
	}
	frame(time.Second)
	if api.reads != 2 {
		t.Fatalf("reads %d after a second", api.reads)
	}
	c.pressKey(env, "esc")
	if !slices.Equal(api.keys, []string{"p1 esc"}) || c.scr.keyErr != "" || time.Until(c.scr.next) > keySettle {
		t.Fatalf("keys %v, next in %v", api.keys, time.Until(c.scr.next))
	}
	env.Online = false
	c.scr.next = time.Time{}
	frame(2 * time.Second)
	if api.reads != 2 {
		t.Fatal("read while offline")
	}
	api.err = errors.New("the tab is guarded")
	c.pressKey(env, "enter")
	if c.scr.keyErr != "The tab is guarded" {
		t.Fatalf("key error %q", c.scr.keyErr)
	}
}

// Stop takes the quiet send button's place while the agent works, and
// interrupts its turn.
func TestStopButton(t *testing.T) {
	working := remote.Tab{Item: remote.Item{State: "working"}}
	for _, tc := range []struct {
		st       sendState
		tab      remote.Tab
		stopping bool
		want     sendState
	}{
		{sendIdle, working, false, sendStop},
		{sendIdle, working, true, sendSending},
		{sendQueue, working, false, sendQueue},
		{sendOff, working, false, sendOff},
		{sendIdle, remote.Tab{Item: remote.Item{State: "completed"}}, false, sendIdle},
	} {
		if got := withStop(tc.st, tc.tab, tc.stopping); got != tc.want {
			t.Errorf("withStop(%d, %q, %v) = %d, want %d", tc.st, tc.tab.State, tc.stopping, got, tc.want)
		}
	}
	api := &screenAPI{}
	c := newChat("p1")
	c.stopTurn(screenEnv(api))
	if !slices.Equal(api.stops, []string{"p1"}) || c.cmp.stopping || c.cmp.notSent != "" {
		t.Fatalf("stops %v, stopping %v, note %q", api.stops, c.cmp.stopping, c.cmp.notSent)
	}
	api.err = errors.New("no chat p1")
	c.stopTurn(screenEnv(api))
	if c.cmp.notSent != "Stop: no chat p1" {
		t.Fatalf("note %q", c.cmp.notSent)
	}
}

func TestScreenKeysAndSize(t *testing.T) {
	n := 0
	for _, r := range screenRows {
		n += r[1] - r[0]
	}
	if n != len(screenKeys) {
		t.Fatalf("rows hold %d keys of %d", n, len(screenKeys))
	}
	for _, k := range screenKeys {
		if !slices.Contains(remote.Keys, k.name) || k.label == "" && k.icon == "" {
			t.Errorf("key %+v", k)
		}
	}
	for _, tc := range []struct {
		text  string
		width float32
		want  float32
	}{
		{"", 400, 13},
		{"short", 400, 13},
		{string(make([]byte, 60)), 396, 11},
		{string(make([]byte, 200)), 396, 9},
	} {
		if got := float32(screenSize(tc.text, tc.width)); got != tc.want {
			t.Errorf("screenSize(%d cols, %v) = %v, want %v", len(tc.text), tc.width, got, tc.want)
		}
	}
}

// The view gives up once its tab leaves the list while online: no read
// goes, and the note names the tab. Offline, the last list stands.
func TestScreenLost(t *testing.T) {
	api := &screenAPI{}
	env := screenEnv(api)
	env.Tab.Tab = "Fix the build"
	c := newChat("p1")
	c.scr.show()
	if c.scr.lost(env) {
		t.Fatal("lost while its tab is listed")
	}
	env.Open, env.Online = false, false
	if c.scr.lost(env) {
		t.Fatal("lost while offline")
	}
	env.Online = true
	if !c.scr.lost(env) || c.scr.lostNote() != "Fix the build closed." {
		t.Fatalf("not lost, note %q", c.scr.lostNote())
	}
	c.readScreen(layout.Context{Ops: new(op.Ops), Now: time.Now()}, env)
	if api.reads != 0 {
		t.Fatal("read the screen of a tab that is gone")
	}
}
