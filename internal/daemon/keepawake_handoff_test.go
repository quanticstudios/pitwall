//go:build linux || darwin

package daemon

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestKeepAwakeChatHandoff(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	root := t.TempDir()
	var oldHeld, newTakes atomic.Int32
	options := func(held *atomic.Int32) func(*Options) {
		return func(o *Options) {
			o.Chats = root
			o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: &stubProvider{}}
			o.NewVT = vt.New
			o.StartPane = func(c pane.Config) (Pane, error) { return pane.Start(c) }
			o.KeepAwake = func() (bool, time.Duration) { return true, 0 }
			o.InhibitIdle = func(context.Context) (io.Closer, error) {
				held.Add(1)
				return awakeCloser(func() error { held.Add(-1); return nil }), nil
			}
		}
	}
	sock, stopOld := runWith(t, f, options(&oldHeld))
	defer stopOld()
	gui := dial(t, sock, "gui")
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "Work"})
	if r.Code != "" {
		t.Fatalf("new chat: %+v", r)
	}
	waitChat(gui, "running chat", func(s model.State) bool { return chatOf(s).State == session.StateRunning })
	waitUntil(t, "old inhibitor", func() bool { return oldHeld.Load() == 1 })
	o := f.options()
	options(&newTakes)(&o)
	checked := make(chan struct{}, 1)
	o.KeepAwake = func() (bool, time.Duration) {
		select {
		case checked <- struct{}{}:
		default:
		}
		return true, 0
	}
	d, ln, err := Takeover(sock, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-checked:
	case <-time.After(3 * time.Second):
		t.Fatal("new daemon did not evaluate keep awake")
	}
	if d.agentsWorking() {
		t.Fatal("an unopened chat retains its handed-off Working activity")
	}
	d.mu.Lock()
	state := d.chats[r.Chat].sum.State
	d.mu.Unlock()
	if state != session.StateReady || newTakes.Load() != 0 {
		t.Fatalf("chat state %s, new inhibitor count %d", state, newTakes.Load())
	}
	waitUntil(t, "old inhibitor released", func() bool { return oldHeld.Load() == 0 })
}
