//go:build linux || darwin

package daemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
	for _, name := range []string{"loaded", "unreadable"} {
		t.Run(name, func(t *testing.T) {
			provider := &stubProvider{}
			f := &fakes{statsCalls: map[string]int{}}
			root := t.TempDir()
			var oldHeld, newTakes atomic.Int32
			options := func(held *atomic.Int32) func(*Options) {
				return func(o *Options) {
					o.Chats = root
					o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: provider}
					o.NewVT = vt.New
					o.StartPane = func(c pane.Config) (Pane, error) { return pane.Start(c) }
					o.KeepAwake = func() (bool, time.Duration) { return true, 0 }
					o.InhibitIdle = func(context.Context) (io.Closer, error) {
						held.Add(1)
						return awakeCloser(func() error { held.Add(-1); return nil }), nil
					}
				}
			}
			old, sock, stopOld := runDaemon(t, f, options(&oldHeld))
			defer stopOld()
			gui := dial(t, sock, "gui")
			activity := func(d *Daemon, id string) (model.Activity, bool) {
				d.mu.Lock()
				defer d.mu.Unlock()
				for _, a := range d.attended() {
					if a.PaneID == id {
						return a, true
					}
				}
				return model.Activity{}, false
			}
			completed := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "Finish"})
			if completed.Code != "" {
				t.Fatalf("completed chat: %+v", completed)
			}
			provider.driver(t).end(session.TurnCompleted)
			waitUntil(t, "completed unseen chat", func() bool {
				a, ok := activity(old, completed.Chat)
				return ok && a.State == model.StateCompleted && a.Unseen
			})
			r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "Work"})
			if r.Code != "" {
				t.Fatalf("new chat: %+v", r)
			}
			waitUntil(t, "running chat", func() bool { a, ok := activity(old, r.Chat); return ok && a.State == model.StateWorking })
			waitUntil(t, "old inhibitor", func() bool { return oldHeld.Load() == 1 })
			if name == "unreadable" {
				if err := os.WriteFile(filepath.Join(root, r.Chat, "config.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
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
			c := d.chats[r.Chat]
			var state string
			if c != nil {
				state = c.sum.State
			}
			d.mu.Unlock()
			if name == "unreadable" {
				if c != nil {
					t.Fatal("chat with unreadable config loaded")
				}
			} else if c == nil || state != session.StateReady {
				t.Fatal("handed-off chat is not ready")
			}
			if newTakes.Load() != 0 {
				t.Fatalf("new inhibitor count %d", newTakes.Load())
			}
			if a, ok := activity(d, completed.Chat); !ok || a.State != model.StateCompleted || !a.Unseen {
				t.Fatalf("completed activity after handoff: %+v, present %v", a, ok)
			}
			waitUntil(t, "old inhibitor released", func() bool { return oldHeld.Load() == 0 })
		})
	}
}
