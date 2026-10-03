package daemon

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestAttention(t *testing.T) {
	defer func(d time.Duration) { noticeInterval = d }(noticeInterval)
	noticeInterval = 50 * time.Millisecond
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{
			Dir: layout.Horizontal, Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}}}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w"}, {ID: "b", WorkspaceID: "w"}},
	}
	o := f.options()
	o.NewVT = vt.New
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	of := func(pane string) *model.Activity {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.snapshot().Activities {
			if a.PaneID == pane {
				return &a
			}
		}
		return nil
	}
	event := func(pane, state string) {
		time.Sleep(time.Millisecond) // a strictly newer UpdatedAt
		if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: pane, Provider: model.ProviderClaude, Payload: []byte(state)}); err != nil {
			t.Fatal(err)
		}
	}
	waitDetail := func(pane, want string) {
		t.Helper()
		for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
			if a := of(pane); a != nil && a.Detail == want {
				return
			}
		}
		t.Fatalf("pane %s: activity %+v, want detail %q", pane, of(pane), want)
	}

	// An OSC 9 from the emulator becomes an unseen awaiting-input activity.
	e := d.notifyingVT("a")(10, 2, io.Discard)
	e.Write([]byte("\x1b]9;tests ✳ passed\x07"))
	waitDetail("a", "tests ✳ passed")
	if a := of("a"); a.State != model.StateAwaitingInput || !a.Unseen || a.Provider != model.ProviderTerminal {
		t.Fatalf("notice %+v", a)
	}
	d.seePane("a")
	if a := of("a"); a != nil {
		t.Fatalf("seen notice still shows: %+v", a)
	}

	// Within noticeInterval only the latest notification is kept, and it
	// shows once the interval ends.
	d.notice("a", vt.Notification{Body: "one"})
	d.notice("a", vt.Notification{Title: "Aider", Body: "two"})
	if a := of("a"); a != nil {
		t.Fatalf("held-back notice shows early: %+v", a)
	}
	waitDetail("a", "Aider: two")

	// Agent states: needs-you is unseen until seen; working never is.
	event("b", string(model.StateAwaitingInput))
	if a := of("b"); !a.Unseen {
		t.Fatalf("new question seen: %+v", a)
	}
	d.seePane("b")
	if a := of("b"); a == nil || a.Unseen {
		t.Fatalf("after SeePane: %+v", a)
	}
	event("b", string(model.StateWorking))
	if a := of("b"); a.Unseen {
		t.Fatalf("working unseen: %+v", a)
	}
	event("b", string(model.StateCompleted))
	if a := of("b"); !a.Unseen {
		t.Fatalf("finished turn seen: %+v", a)
	}

	// A newer agent state replaces a notification for good.
	time.Sleep(noticeInterval)
	d.notice("b", vt.Notification{Body: "ping"})
	waitDetail("b", "ping")
	event("b", string(model.StateWorking))
	if a := of("b"); a.State != model.StateWorking {
		t.Fatalf("notice outlived a newer state: %+v", a)
	}
	event("b", "clear")
	if a := of("b"); a != nil {
		t.Fatalf("notice came back: %+v", a)
	}
}
