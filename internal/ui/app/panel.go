package app

import (
	"context"
	"image"
	"slices"
	"sync"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

const (
	// minPaneArea is the width the panes keep before the side panel
	// shrinks, then hides.
	minPaneArea unit.Dp = 360
	// filesEvery is how often the open panel asks git for the changed files.
	filesEvery = 3 * time.Second
	filesWait  = 10 * time.Second
)

// panelWidth is the side panel's width in a window with avail pixels right
// of the sidebar: want, shrunk so the panes keep keep, and 0 when that
// leaves less than least.
func panelWidth(avail, want, least, keep int) int {
	w := min(want, avail-keep)
	if w < least {
		return 0
	}
	return w
}

// sidePanel is one window's side panel and what it reads: the agent's feed
// from flow.Watch and the changed files from gitstat.Files, both filled in
// from their own goroutines. The UI goroutine never waits on either.
type sidePanel struct {
	view panel.Panel

	mu        sync.Mutex
	watching  string // pane id, provider and transcript Watch follows, "" for none
	stopWatch context.CancelFunc
	watchGen  int
	feed      *flow.Feed

	filesDir  string // the directory listed, "" for none
	stopFiles context.CancelFunc
	filesGen  int
	base      string
	files     []gitstat.FileStat
	git       bool
}

// follow points the panel at pane, whose live directory is dir: a pane,
// provider or transcript change cancels the old Watch and starts a new one, a
// directory change restarts the file listing. A nil pane and "" stop
// both. invalidate is called after new data lands.
func (s *sidePanel) follow(p *model.Pane, dir string, invalidate func()) {
	key := ""
	if p != nil && p.Transcript != "" && p.Provider != "" {
		key = p.ID + "\x00" + string(p.Provider) + "\x00" + p.Transcript
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != s.watching {
		if s.stopWatch != nil {
			s.stopWatch()
			s.stopWatch = nil
		}
		s.watchGen++
		s.watching, s.feed = key, nil
		if key != "" {
			ctx, cancel := context.WithCancel(context.Background())
			s.stopWatch = cancel
			gen, provider, path := s.watchGen, p.Provider, p.Transcript
			go watchFeed(ctx, provider, path, func(f flow.Feed) {
				s.mu.Lock()
				if s.watchGen == gen {
					s.feed = &f
				}
				s.mu.Unlock()
				invalidate()
			})
		}
	}
	if dir != s.filesDir {
		if s.stopFiles != nil {
			s.stopFiles()
			s.stopFiles = nil
		}
		s.filesGen++
		s.filesDir, s.base, s.files, s.git = dir, "", nil, false
		if dir != "" {
			ctx, cancel := context.WithCancel(context.Background())
			s.stopFiles = cancel
			go s.listFiles(ctx, s.filesGen, dir, invalidate)
		}
	}
}

// listFiles lists dir's changed files every filesEvery until ctx is done,
// and invalidates when the list changed.
func (s *sidePanel) listFiles(ctx context.Context, gen int, dir string, invalidate func()) {
	t := time.NewTicker(filesEvery)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, filesWait)
		base, files, err := listFiles(c, dir)
		cancel()
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		changed := s.filesGen == gen && (s.base != base || s.git != (err == nil) || !slices.Equal(s.files, files))
		if changed {
			s.base, s.files, s.git = base, files, err == nil
		}
		s.mu.Unlock()
		if changed {
			invalidate()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *sidePanel) stop() { s.follow(nil, "", nil) }

// layoutPanel draws the side panel in r for the focused pane and keeps its
// readers following that pane; with r empty it stops them.
func (u *ui) layoutPanel(gtx gl.Context, st *model.State, r image.Rectangle) {
	if r.Empty() {
		u.panel.stop()
		return
	}
	in := panel.Input{Pane: findPane(st, u.nav.focused()), Decide: st.Decide.Provider, Now: gtx.Now, ShowCost: u.cfg.ShowCost}
	dir := ""
	if ws := findWorkspace(st, u.nav.workspace); ws != nil {
		in.Branch = st.Stats[ws.ID]
		dir = ws.Path
	}
	if in.Pane != nil {
		for i := range st.Activities {
			if st.Activities[i].PaneID == in.Pane.ID {
				in.Activity = &st.Activities[i]
			}
		}
		if in.Pane.Cwd != "" {
			dir = in.Pane.Cwd
		}
	}
	invalidate := u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	u.panel.follow(in.Pane, dir, invalidate)
	u.panel.mu.Lock()
	in.Feed, in.Base, in.Files, in.Git = u.panel.feed, u.panel.base, u.panel.files, u.panel.git
	u.panel.mu.Unlock()

	paint.FillShape(gtx.Ops, u.th.Border, clip.Rect{Min: r.Min, Max: image.Pt(r.Min.X+1, r.Max.Y)}.Op())
	r.Min.X++
	off := op.Offset(r.Min).Push(gtx.Ops)
	pgtx := gtx
	pgtx.Constraints = gl.Exact(r.Size())
	u.panel.view.Layout(pgtx, u.th, in)
	off.Pop()
	if f, ok := u.panel.view.Diff(); ok && dir != "" && in.Base != "" {
		u.nav.expectPane(st)
		u.send(proto.OpenPane{WorkspaceID: u.nav.workspace, Dir: layout.Horizontal, Cmd: diffCmd(dir, in.Base, &f)})
	}
}
