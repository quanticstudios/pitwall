package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

type resumedHistoryPane struct {
	*emuPane
	code int
}

func (p *resumedHistoryPane) ExitCode() int { return p.code }

func TestResumeFallbackKeepsHistory(t *testing.T) {
	for _, code := range []int{0, 1} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			st := restoreState(t)
			st.Panes, st.Workspaces, st.Sessions = st.Panes[:1], st.Workspaces[:1], st.Sessions[:1]
			if code != 0 {
				st.Panes[0].Cmd = []string{"claude"}
			}
			h, err := store.PackHistory(80, "precious old output\r\n", time.Unix(1, 0))
			if err != nil {
				t.Fatal(err)
			}
			st.Panes[0].History = h
			f := &fakes{saved: st}
			started := make(chan *resumedHistoryPane, 2)
			d, _, stop := runDaemon(t, f, func(o *Options) {
				historyOptions(o)
				o.StartPane = func(c pane.Config) (Pane, error) {
					p := &resumedHistoryPane{emuPane: &emuPane{e: c.NewVT(c.Cols, c.Rows, io.Discard), dirty: make(chan struct{}, 1), done: make(chan struct{})}, code: code}
					if len(c.Cmd) > 0 {
						p.Write2("resume message\r\n")
					} else {
						p.Write2("shell ready\r\n")
					}
					started <- p
					return p, nil
				}
			})
			defer stop()
			first := <-started
			first.Close()
			var shell *resumedHistoryPane
			select {
			case shell = <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("fallback shell did not start")
			}
			_, text := shell.HistoryText(2000)
			for _, want := range []string{"precious old output", "resume message", "shell ready"} {
				if !strings.Contains(text, want) {
					t.Fatalf("fallback lost %q: %q", want, text)
				}
			}
			if strings.Count(text, "Restored from") != 1 {
				t.Fatalf("fallback added a divider: %q", text)
			}
			d.save()
			f.mu.Lock()
			checkpoint := f.saved.Panes[0].History
			f.mu.Unlock()
			persisted, err := store.UnpackHistory(checkpoint)
			if err != nil || !strings.Contains(persisted, "precious old output") {
				t.Fatalf("fallback dropped checkpoint: %q, %v", persisted, err)
			}
			d.Save()
			f.mu.Lock()
			saved := f.saved.Panes[0].History
			f.mu.Unlock()
			text, err = store.UnpackHistory(saved)
			if err != nil || !strings.Contains(text, "precious old output") || !strings.Contains(text, "resume message") {
				t.Fatalf("fallback save: %q, %v", text, err)
			}
		})
	}
}

type blockedHistoryPane struct {
	*emuPane
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
}

func (p *blockedHistoryPane) HistoryText(limit int) (int, string) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return p.emuPane.HistoryText(limit)
}

func TestHistoryCaptureReleasesDaemonAndRoutineSaveReusesCache(t *testing.T) {
	f := &fakes{}
	o := f.options()
	historyOptions(&o)
	st := restoreState(t)
	st.Panes, st.Workspaces, st.Sessions = st.Panes[:1], st.Workspaces[:1], st.Sessions[:1]
	d := fromState(o, st)
	p := &blockedHistoryPane{emuPane: &emuPane{e: vt.New(80, 24, io.Discard)}, entered: make(chan struct{}), release: make(chan struct{})}
	p.Write2("cached output\r\n")
	d.panes[st.Panes[0].ID] = p
	done := make(chan struct{})
	go func() { d.Save(); close(done) }()
	var once sync.Once
	release := func() { once.Do(func() { close(p.release) }) }
	defer func() { release(); <-done }()
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("capture did not start")
	}
	locked := make(chan struct{})
	go func() { d.mu.Lock(); d.mu.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("capture holds d.mu")
	}
	release()
	<-done
	f.mu.Lock()
	cached := f.saved.Panes[0].History
	f.mu.Unlock()
	if cached == nil {
		t.Fatal("checkpoint omitted history")
	}
	p.Write2("new output\r\n")
	d.save()
	f.mu.Lock()
	routine := f.saved.Panes[0].History
	f.mu.Unlock()
	if p.calls.Load() != 1 || routine == nil || !bytes.Equal(cached.Data, routine.Data) {
		t.Fatalf("routine save recaptures: calls %d", p.calls.Load())
	}
	d.Save()
	f.mu.Lock()
	fresh := f.saved.Panes[0].History
	f.mu.Unlock()
	text, err := store.UnpackHistory(fresh)
	if err != nil || !strings.Contains(text, "new output") || p.calls.Load() != 2 {
		t.Fatalf("fresh checkpoint: %q, %v, calls %d", text, err, p.calls.Load())
	}
}

func TestHistoryCaptureRechecksPrivacy(t *testing.T) {
	f := &fakes{}
	o := f.options()
	historyOptions(&o)
	var limit atomic.Int32
	limit.Store(2000)
	o.PersistScrollback = func() int { return int(limit.Load()) }
	st := restoreState(t)
	st.Panes, st.Workspaces, st.Sessions = st.Panes[:1], st.Workspaces[:1], st.Sessions[:1]
	d := fromState(o, st)
	p := &blockedHistoryPane{emuPane: &emuPane{e: vt.New(80, 24, io.Discard)}, entered: make(chan struct{}), release: make(chan struct{})}
	p.Write2("sensitive output\r\n")
	d.panes[st.Panes[0].ID] = p
	done := make(chan struct{})
	go func() { d.Save(); close(done) }()
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("capture did not start")
	}
	limit.Store(0)
	close(p.release)
	<-done
	f.mu.Lock()
	saved := f.saved.Panes[0].History
	f.mu.Unlock()
	if saved != nil {
		t.Fatal("capture saved history after policy disabled it")
	}
}

func TestHeldHistoryDoesNotAccumulateNotices(t *testing.T) {
	st := restoreState(t)
	st.Panes, st.Workspaces = st.Panes[5:], st.Workspaces[5:]
	st.Panes[0].Held, st.Panes[0].Exited = true, true
	h, err := store.PackHistory(80, "held command output\r\n", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	st.Panes[0].History = h
	for range 3 {
		f := &fakes{saved: st}
		o := f.options()
		historyOptions(&o)
		d, err := NewWith(o)
		if err != nil {
			t.Fatal(err)
		}
		p := d.panes[st.Panes[0].ID].(*stopped)
		_, screen := vt.HistoryText(p.vt, 2000)
		if strings.Count(screen, "Restored from") != 1 || strings.Count(screen, "before pitwall restarted") != 1 {
			t.Fatalf("stacked annotations: %q", screen)
		}
		d.Save()
		f.mu.Lock()
		st = f.saved
		f.mu.Unlock()
		text, err := store.UnpackHistory(st.Panes[0].History)
		if err != nil || strings.Contains(text, "Restored from") || strings.Contains(text, "before pitwall restarted") || !strings.Contains(text, "held command output") {
			t.Fatalf("saved annotations: %q, %v", text, err)
		}
		d.shutdown()
	}
}

func BenchmarkPaneHistorySaves(b *testing.B) {
	st := fanoutState(20)
	o := (&fakes{}).options()
	o.NewVT = vt.New
	o.PersistScrollback = func() int { return 2000 }
	path := filepath.Join(b.TempDir(), "state.json")
	o.Save = func(s model.State) error { return store.Save(path, s) }
	d := fromState(o, st)
	var seed uint32 = 1
	for _, sp := range st.Panes {
		e := vt.New(200, 24, io.Discard)
		for range 2200 {
			var line strings.Builder
			for segment := range 5 {
				fmt.Fprintf(&line, "\x1b[38;5;%dm", segment+1)
				for range 40 {
					seed = seed*1664525 + 1013904223
					line.WriteByte(byte(33 + seed%90))
				}
			}
			line.WriteString("\x1b[0m\r\n")
			e.Write([]byte(line.String()))
		}
		d.panes[sp.ID] = &emuPane{e: e}
		b.Cleanup(func() { e.(io.Closer).Close() })
	}
	d.Save()
	for _, checkpoint := range []bool{false, true} {
		name := "routine"
		if checkpoint {
			name = "checkpoint"
		}
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if checkpoint {
					d.Save()
				} else {
					d.save()
				}
			}
			fi, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(fi.Size()), "state-bytes")
		})
	}
}
