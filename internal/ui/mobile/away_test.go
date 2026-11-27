package mobile

import (
	"context"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/remote"
)

// viewAPI records the chat's View beats.
type viewAPI struct {
	chatAPI
	views []bool
}

func (v *viewAPI) View(_ context.Context, pane string, on bool) error {
	v.views = append(v.views, on)
	return nil
}

// TestViewBeat: the open chat beats that it is on screen once, then every
// phoneBeat, only while the app has the foreground, the tab is open and
// the stream works; back in the foreground it beats again at once.
func TestViewBeat(t *testing.T) {
	api := &viewAPI{}
	c := newChat("p1")
	now := time.Unix(1_800_000_000, 0)
	env := chatEnv{Tab: remote.Tab{Item: remote.Item{Pane: "p1"}}, Open: true, Online: true, Foreground: true, Now: now,
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			done(call(context.Background(), api))
		}}
	beat := func() {
		gtx := layout.Context{Ops: new(op.Ops), Now: env.Now}
		c.viewBeat(gtx, env)
	}
	beat()
	beat()
	env.Now = now.Add(phoneBeat - time.Second)
	beat()
	if len(api.views) != 1 || !api.views[0] {
		t.Fatalf("beats within phoneBeat: %v", api.views)
	}
	env.Now = now.Add(phoneBeat)
	beat()
	if len(api.views) != 2 {
		t.Fatalf("no beat after phoneBeat: %v", api.views)
	}
	env.Foreground = false
	env.Now = now.Add(3 * phoneBeat)
	beat()
	if len(api.views) != 2 {
		t.Fatalf("a beat in the background: %v", api.views)
	}
	env.Foreground = true
	env.Now = now.Add(3*phoneBeat + time.Second)
	beat()
	if len(api.views) != 3 {
		t.Fatalf("no beat at once back in the foreground: %v", api.views)
	}
	env.Online = false
	env.Now = now.Add(5 * phoneBeat)
	beat()
	if len(api.views) != 3 {
		t.Fatalf("a beat with the stream down: %v", api.views)
	}
}
