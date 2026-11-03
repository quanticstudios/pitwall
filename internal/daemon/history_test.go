package daemon

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func (p *emuPane) HistoryText(limit int) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return vt.HistoryText(p.e, limit)
}

func historyOptions(o *Options) {
	o.NewVT = vt.New
	o.PersistScrollback = func() int { return 2000 }
	o.StartPane = func(c pane.Config) (Pane, error) {
		e := c.NewVT(c.Cols, c.Rows, io.Discard)
		e.Write([]byte("fresh output\r\n"))
		return &emuPane{e: e, dirty: make(chan struct{}, 1), done: make(chan struct{})}, nil
	}
}

func TestPaneHistorySaveRestore(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	d, _, stop := runDaemon(t, f, historyOptions)
	d.mu.Lock()
	h := d.panes["shell"].(*emuPane)
	h.Write2("oldest\r\n\x1b[31msaved red\x1b[0m\r\nlast screen")
	d.mu.Unlock()
	d.Save()
	f.mu.Lock()
	s := f.saved
	f.mu.Unlock()
	history := s.Panes[1].History
	if history == nil {
		t.Fatal("save omitted pane history")
	}
	text, err := store.UnpackHistory(history)
	if err != nil || !strings.Contains(text, "saved red") || !strings.Contains(text, "last screen") {
		t.Fatalf("capture %q: %v", text, err)
	}
	d.mu.Lock()
	live := d.snapshot()
	d.mu.Unlock()
	for _, p := range live.Panes {
		if p.History != nil {
			t.Fatal("live state includes history")
		}
	}
	// Freeze is logind's shutdown-save path; stop is Serve's final save.
	d.Freeze()
	stop()
	f.mu.Lock()
	s = f.saved
	f.mu.Unlock()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := store.Save(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f2 := &fakes{saved: loaded}
	d2, _, stop2 := runDaemon(t, f2, historyOptions)
	defer stop2()
	d2.mu.Lock()
	p := d2.panes["shell"].(*emuPane)
	p.mu.Lock()
	_, text = vt.HistoryText(p.e, 2000)
	p.mu.Unlock()
	d2.mu.Unlock()
	old, divider, fresh := strings.Index(text, "saved red"), strings.Index(text, "Restored from "), strings.LastIndex(text, "fresh output")
	if old < 0 || divider < old || fresh < divider {
		t.Fatalf("restore order %q", text)
	}
	if !strings.Contains(text, "\x1b[2mRestored from ") {
		t.Fatalf("divider is not dim: %q", text)
	}
	if strings.Count(text, "Restored from ") != 1 {
		t.Fatalf("duplicate divider: %q", text)
	}
}

func TestHistoryPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		env   []string
		want  bool
	}{
		{"enabled", 2000, nil, true}, {"off", 0, nil, false},
		{"guarded", 2000, []string{"PITWALL_GUARD=1"}, false},
		{"excluded", 2000, []string{"PITWALL_NO_HISTORY=1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakes{saved: restoreState(t)}
			f.saved.Panes[1].Env = tc.env
			h, err := store.PackHistory(80, "private old text\r\n", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.saved.Panes[1].History = h
			d, _, stop := runDaemon(t, f, func(o *Options) { historyOptions(o); o.PersistScrollback = func() int { return tc.limit } })
			defer stop()
			d.mu.Lock()
			p := d.panes["shell"].(*emuPane)
			p.mu.Lock()
			_, text := vt.HistoryText(p.e, 2000)
			p.mu.Unlock()
			d.mu.Unlock()
			if strings.Contains(text, "private old text") != tc.want {
				t.Fatalf("restore privacy %q", text)
			}
			d.Save()
			f.mu.Lock()
			saved := f.saved.Panes[1].History
			f.mu.Unlock()
			if (saved != nil) != tc.want {
				t.Fatalf("saved history %v", saved != nil)
			}
		})
	}
	// One guarded pane excludes every pane in its tab, including shell splits.
	d := &Daemon{o: Options{PersistScrollback: func() int { return 2000 }}, st: model.State{Panes: []model.Pane{{ID: "guard", WorkspaceID: "w", Env: []string{"PITWALL_GUARD=1"}}, {ID: "shell", WorkspaceID: "w"}}}}
	if d.historyLines(d.st.Panes[1]) != 0 {
		t.Fatal("guarded split saves history")
	}
}

func TestHeldAndQueuedHistory(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	h, err := store.PackHistory(80, "held output\r\n", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.saved.Panes[5].Held = true
	f.saved.Panes[5].Exited = true
	f.saved.Panes[5].History = h
	f.saved.Panes[0].History = h
	d, _, stop := runDaemon(t, f, func(o *Options) {
		historyOptions(o)
		o.StartupPerAgentDelay = func() time.Duration { return time.Hour }
	})
	defer stop()
	d.mu.Lock()
	held := d.panes["command"].(*stopped)
	_, text := held.HistoryText(2000)
	s := d.saveSnapshot()
	d.mu.Unlock()
	if !strings.Contains(text, "held output") {
		t.Fatalf("held %q", text)
	}
	if s.Panes[0].History == nil {
		t.Fatal("queued history lost")
	}
	f.mu.Lock()
	f.saved = s
	f.mu.Unlock()
}

func TestHistoryLoopSavesOutput(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	d, _, stop := runDaemon(t, f, historyOptions)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.historyLoop(ctx, time.Millisecond); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		got := f.saved.Panes[1].History != nil
		f.mu.Unlock()
		if got {
			break
		}
		select {
		case <-deadline:
			t.Fatal("history loop did not save")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestHistoryRestoreCorruptAndLowerLimit(t *testing.T) {
	d := &Daemon{o: Options{PersistScrollback: func() int { return 2 }}}
	h, err := store.PackHistory(80, "drop\r\nkeep\r\nnewest\r\n", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e := d.restoredVT(model.Pane{History: h}, vt.New)(80, 24, io.Discard)
	_, text := vt.HistoryText(e, 2000)
	if strings.Contains(text, "drop") || !strings.Contains(text, "keep") || !strings.Contains(text, "newest") {
		t.Fatalf("limit %q", text)
	}
	h.Data = []byte("broken")
	e = d.restoredVT(model.Pane{History: h}, vt.New)(80, 24, io.Discard)
	_, text = vt.HistoryText(e, 2000)
	if strings.Contains(text, "Restored from") {
		t.Fatal("corrupt history restored")
	}
}
