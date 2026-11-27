package mobile

import (
	"context"
	"testing"

	"github.com/quanticstudios/pitwall/internal/remote"
)

// The pill says Needs you while the tab waits on the user, else its label.
func TestPillState(t *testing.T) {
	th := Theme()
	for _, c := range []struct {
		tab  remote.Tab
		open bool
		want string
	}{
		{remote.Tab{Item: remote.Item{State: "awaiting-input", Label: "Input"}}, true, "Needs you"},
		{remote.Tab{Item: remote.Item{State: "pending-approval"}}, true, "Needs you"},
		{remote.Tab{Item: remote.Item{State: "working", Label: "Working"}}, true, "Working"},
		{remote.Tab{Item: remote.Item{State: "completed"}}, true, "Done"},
		{remote.Tab{Item: remote.Item{State: "completed", Label: "Exited 1"}, Exited: true}, true, "Exited 1"},
		{remote.Tab{}, true, ""},
		{remote.Tab{Item: remote.Item{State: "working"}}, false, "Closed"},
	} {
		if got, _ := pillState(th, c.tab, c.open); got != c.want {
			t.Errorf("pillState(%q, %v) = %q, want %q", c.tab.State, c.open, got, c.want)
		}
	}
	if _, col := pillState(th, remote.Tab{Item: remote.Item{State: "awaiting-input"}}, true); col != th.Yellow {
		t.Errorf("Needs you is %v, want amber %v", col, th.Yellow)
	}
}

// queueAPI records the queue calls the composer makes.
type queueAPI struct {
	demoAPI
	got []string
	idx []int
}

func (q *queueAPI) Queue(_ context.Context, pane, id, action string, index int) error {
	q.got = append(q.got, pane+" "+id+" "+action)
	q.idx = append(q.idx, index)
	return nil
}

// Move up puts a row one place nearer the front; Remove drops it.
func TestQueueDo(t *testing.T) {
	c := newChat("p1")
	api := &queueAPI{}
	env := chatEnv{Tab: remote.Tab{Item: remote.Item{Pane: "p1"}}, Open: true,
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			done(call(context.Background(), api))
		}}
	c.queueDo(env, "q2", "move", 0)
	c.queueDo(env, "q1", "remove", 0)
	if len(api.got) != 2 || api.got[0] != "p1 q2 move" || api.idx[0] != 0 || api.got[1] != "p1 q1 remove" {
		t.Fatalf("queue calls %v %v", api.got, api.idx)
	}
}

// The status row shows the agent's spinner row, or why the queue waits.
func TestStatusLine(t *testing.T) {
	c := newChat("p1")
	env := chatEnv{Open: true, Tab: remote.Tab{Item: remote.Item{State: "working"}, Spinner: "Thinking… 11s · ↓ 789"}}
	if s, w := c.statusLine(env); s != "Thinking… 11s · ↓ 789" || !w {
		t.Errorf("working: %q %v", s, w)
	}
	env.Tab = remote.Tab{Item: remote.Item{State: "completed"}, QueueHold: "the agent asks", Queue: []remote.QueueRow{{ID: "a"}}}
	if s, w := c.statusLine(env); s != "Waiting: the agent asks" || w {
		t.Errorf("held: %q %v", s, w)
	}
	env.Tab.Queue = nil
	if s, _ := c.statusLine(env); s != "" {
		t.Errorf("idle: %q", s)
	}
	if composerHint("", true, true) != "Enter adds a line · / for commands" || composerHint("bad pick", true, true) != "bad pick" {
		t.Error("composerHint")
	}
}
