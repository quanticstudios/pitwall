package web

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

type richControl struct {
	*fakeControl
	evMu      sync.Mutex
	evWatches map[*func(ControlEvent)]bool
	sentKeys  []struct {
		pane string
		keys []string
	}
	guardPane string
	ansiText  string
	htmlText  string
}

func newRichControl(st model.State) *richControl {
	return &richControl{
		fakeControl: &fakeControl{st: st},
		evWatches:   map[*func(ControlEvent)]bool{},
		ansiText:    "\x1b[31mred\x1b[0m",
		htmlText:    `<pre class="pitwall-screen"><span>red</span></pre>`,
	}
}

func (r *richControl) ReadFormat(pane string, screen bool, format string) (string, error) {
	if !screen {
		return r.Read(pane, false)
	}
	switch format {
	case "", "text":
		return r.Read(pane, true)
	case "ansi":
		return r.ansiText, nil
	case "html":
		return r.htmlText, nil
	default:
		return "", errors.New("unsupported format")
	}
}

func (r *richControl) ScrollbackFormat(pane string, lines int, format string) (string, error) {
	switch format {
	case "", "text":
		return r.Scrollback(pane, lines)
	case "ansi":
		return r.ansiText + "\n", nil
	case "html":
		return r.htmlText, nil
	default:
		return "", errors.New("unsupported format")
	}
}

func (r *richControl) SendKeys(_ context.Context, pane string, keys []string) error {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	if pane == r.guardPane {
		return ctlErr{"refused", "guarded tab"}
	}
	cp := append([]string(nil), keys...)
	r.sentKeys = append(r.sentKeys, struct {
		pane string
		keys []string
	}{pane: pane, keys: cp})
	return nil
}

func (r *richControl) WatchEvents(fn func(ControlEvent)) func() {
	r.evMu.Lock()
	r.evWatches[&fn] = true
	r.evMu.Unlock()
	return func() {
		r.evMu.Lock()
		delete(r.evWatches, &fn)
		r.evMu.Unlock()
	}
}

func (r *richControl) emit(ev ControlEvent) {
	r.evMu.Lock()
	fns := make([]func(ControlEvent), 0, len(r.evWatches))
	for p := range r.evWatches {
		fns = append(fns, *p)
	}
	r.evMu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
}

func TestControlEventsStream(t *testing.T) {
	rc := newRichControl(twoSessions())
	c := dialLines(t, &Server{Control: rc})
	c.send(t, linesHello(1))
	c.json(t)

	r := ask(t, c, `{"type":"subscribeEvents","id":"e1"}`)
	if r["ok"] != true {
		t.Fatalf("subscribeEvents failed: %v", r)
	}

	// 1. Agent state change with detail.
	rc.set(func(st *model.State) {
		st.Activities[0].State = model.StatePendingApproval
		st.Activities[0].Detail = "Run go test ./..."
	})
	ev := c.json(t)
	checkFrame(t, ev)
	if ev["type"] != "event" || ev["event"] != "pane.state" || ev["pane"] != "p1" || ev["state"] != "pending-approval" || ev["detail"] != "Run go test ./..." {
		t.Fatalf("pane.state event = %v", ev)
	}

	// 2. Title change.
	rc.set(func(st *model.State) {
		st.Workspaces[0].Name = "renamed-api"
	})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] != "title" || ev["tab"] != "w1" || ev["title"] != "renamed-api" {
		t.Fatalf("title event = %v", ev)
	}

	// 3. PR state change.
	rc.set(func(st *model.State) {
		st.PRs = map[string]model.PR{
			"w1": {Number: 42, State: model.PROpen, Reason: model.ReasonChecksFailing, Detail: "1 failing"},
		}
	})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] != "pr.state" || ev["tab"] != "w1" || ev["prState"] != "open" || ev["prReason"] != "checks_failing" || ev["prNumber"] != float64(42) {
		t.Fatalf("pr.state event = %v", ev)
	}

	// 4. Tab opened and closed.
	rc.set(func(st *model.State) {
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: "w9", SessionID: "m1", Name: "new-tab"})
	})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] != "tab.opened" || ev["tab"] != "w9" || ev["title"] != "new-tab" {
		t.Fatalf("tab.opened event = %v", ev)
	}
	rc.set(func(st *model.State) {
		st.Workspaces = st.Workspaces[:len(st.Workspaces)-1]
	})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] != "tab.closed" || ev["tab"] != "w9" {
		t.Fatalf("tab.closed event = %v", ev)
	}

	// 5. Direct daemon events: queued.sent and turn.end.
	rem := 1
	rc.emit(ControlEvent{
		At:      time.Now().UTC(),
		Event:   "queued.sent",
		Session: "m1",
		Tab:     "w1",
		Pane:    "p1",
		From:    "p2",
		Queued:  &rem,
	})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] != "queued.sent" || ev["from"] != "p2" || ev["queued"] != float64(1) {
		t.Fatalf("queued.sent event = %v", ev)
	}

	rc.emit(ControlEvent{
		At:      time.Now().UTC(),
		Event:   "turn.end",
		Session: "m1",
		Tab:     "w1",
		Pane:    "p1",
		Agent:   "claude",
		State:   "done",
		Detail:  "all tests pass",
	})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] != "turn.end" || ev["agent"] != "claude" || ev["state"] != "done" || ev["detail"] != "all tests pass" {
		t.Fatalf("turn.end event = %v", ev)
	}

	// 6. Pane exited (emits pane.state exited + pane.exited with exitCode).
	rc.set(func(st *model.State) {
		st.Panes[1].Exited = true
		st.Panes[1].ExitCode = 7
	}, "p2", 7)
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["event"] == "pane.state" {
		ev = c.json(t)
		checkFrame(t, ev)
	}
	if ev["event"] != "pane.exited" || ev["pane"] != "p2" || ev["exitCode"] != float64(7) {
		t.Fatalf("pane.exited event = %v", ev)
	}

	// 7. Pane filter suppresses other panes' events and passes matching pane.
	r = ask(t, c, `{"type":"subscribeEvents","id":"e2","pane":"@p1"}`)
	if r["ok"] != true {
		t.Fatalf("replace subscribeEvents failed: %v", r)
	}
	rc.emit(ControlEvent{Event: "turn.end", Session: "m1", Tab: "w2", Pane: "p2", Agent: "codex", State: "done"})
	rc.emit(ControlEvent{Event: "turn.end", Session: "m1", Tab: "w1", Pane: "p1", Agent: "claude", State: "error", Detail: "boom"})
	ev = c.json(t)
	checkFrame(t, ev)
	if ev["pane"] != "p1" || ev["state"] != "error" || ev["detail"] != "boom" {
		t.Fatalf("filtered event = %v", ev)
	}

	// 8. Unsubscribe.
	r = ask(t, c, `{"type":"unsubscribeEvents","id":"u1"}`)
	if r["ok"] != true {
		t.Fatalf("unsubscribeEvents failed: %v", r)
	}
}

func TestControlSendKeyAndFormats(t *testing.T) {
	rc := newRichControl(twoSessions())
	rc.guardPane = "p2"
	c := dialLines(t, &Server{Control: rc})
	c.send(t, linesHello(1))
	c.json(t)

	// Valid sendKey.
	r := ask(t, c, `{"type":"sendKey","id":"k1","pane":"@p1","keys":["up","enter","ctrl+c"]}`)
	checkFrame(t, r)
	if r["ok"] != true {
		t.Fatalf("sendKey failed: %v", r)
	}
	rc.evMu.Lock() // the server's goroutine appends under it
	sent := slices.Clone(rc.sentKeys)
	rc.evMu.Unlock()
	if len(sent) != 1 || sent[0].pane != "p1" || strings.Join(sent[0].keys, ",") != "up,enter,ctrl+c" {
		t.Fatalf("sentKeys = %+v", sent)
	}

	// Empty keys -> invalid.
	r = ask(t, c, `{"type":"sendKey","id":"k2","pane":"@p1","keys":[]}`)
	if r["ok"] != false || r["code"] != "invalid" {
		t.Fatalf("empty keys = %v, want invalid", r)
	}

	// Unknown key -> invalid.
	r = ask(t, c, `{"type":"sendKey","id":"k3","pane":"@p1","keys":["notakey"]}`)
	if r["ok"] != false || r["code"] != "invalid" {
		t.Fatalf("unknown key = %v, want invalid", r)
	}

	// Guarded tab -> refused.
	r = ask(t, c, `{"type":"sendKey","id":"k4","pane":"@p2","keys":["enter"]}`)
	if r["ok"] != false || r["code"] != "refused" {
		t.Fatalf("guarded pane = %v, want refused", r)
	}

	// Formatted readPane and scrollback.
	r = ask(t, c, `{"type":"readPane","id":"r1","pane":"@p1","screen":true,"format":"ansi"}`)
	if r["ok"] != true || data(r)["text"] != rc.ansiText {
		t.Fatalf("readPane ansi = %v", r)
	}
	r = ask(t, c, `{"type":"readPane","id":"r2","pane":"@p1","screen":true,"format":"html"}`)
	if r["ok"] != true || data(r)["text"] != rc.htmlText {
		t.Fatalf("readPane html = %v", r)
	}
	r = ask(t, c, `{"type":"readPane","id":"r3","pane":"@p1","screen":false,"format":"html"}`)
	if r["ok"] != false || r["code"] != "invalid" {
		t.Fatalf("readPane non-screen html = %v, want invalid", r)
	}
	r = ask(t, c, `{"type":"readPane","id":"r4","pane":"@p1","screen":true,"format":"bogus"}`)
	if r["ok"] != false || r["code"] != "invalid" {
		t.Fatalf("readPane bogus format = %v, want invalid", r)
	}
	r = ask(t, c, `{"type":"scrollback","id":"s1","pane":"@p1","lines":10,"format":"ansi"}`)
	if r["ok"] != true || data(r)["text"] != rc.ansiText+"\n" {
		t.Fatalf("scrollback ansi = %v", r)
	}
	r = ask(t, c, `{"type":"scrollback","id":"s2","pane":"@p1","format":"html"}`)
	if r["ok"] != true || data(r)["text"] != rc.htmlText {
		t.Fatalf("scrollback html = %v", r)
	}
	r = ask(t, c, `{"type":"scrollback","id":"s3","pane":"@p1","format":"bogus"}`)
	if r["ok"] != false || r["code"] != "invalid" {
		t.Fatalf("scrollback bogus format = %v, want invalid", r)
	}
}
