package app

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pointerChatBackend serves a chat rich with clickables (links, tool rows,
// work logs, approval buttons, minimap, queue, composer chips and send).
type pointerChatBackend struct {
	*FakeBackend
}

func (b pointerChatBackend) Chat(string) (session.Snapshot, bool) {
	in := func(m map[string]any) json.RawMessage { raw, _ := json.Marshal(m); return raw }
	at := func(s int) string { return time.Date(2026, 11, 22, 12, 0, s, 0, time.UTC).Format(time.RFC3339Nano) }
	turn, turn2, zero := "t1", "t2", 0
	modelName := "claude-sonnet-4-6"
	effort := "high"
	used, total := int64(42000), int64(200000)
	reply := "See [pitwall](https://github.com/quanticstudios/pitwall) for details.\n\nPlain selectable paragraph text here."
	diff := "@@ -1,2 +1,3 @@\n package kit\n+// pointer cursor\n"
	return session.Snapshot{
		SessionID:       "c1",
		Provider:        model.ProviderClaude,
		SessionState:    session.StateWaiting,
		Running:         true,
		Model:           &modelName,
		Effort:          &effort,
		PermissionMode:  "default",
		InteractionMode: "build",
		Usage:           &session.Usage{ContextTokens: &used, MaxTokens: &total},
		QueuedInputs:    []session.QueuedInput{{ID: "q1", Text: "Follow up after this turn", CreatedAt: at(40)}},
		PendingApprovals: []session.Approval{{
			ID: "a1", RequestType: session.RequestFileChange,
			Request: &session.ToolRequest{ToolName: "Edit", FilePath: "/repo/internal/ui/kit/clickable.go", Diff: diff},
		}},
		Content: []session.Item{
			{ID: "p1", Kind: session.KindUserPrompt, Text: "Add pointer cursor to every clickable.", TurnID: &turn, CreatedAt: at(0)},
			{ID: "c1", Kind: session.KindToolCall, TurnID: &turn, CreatedAt: at(5), UpdatedAt: at(10),
				Tool: &session.ToolCall{CallID: "c1", ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "go test ./..."}), ExitCode: &zero, Output: "ok", StartedAt: at(5), EndedAt: at(10)}},
			{ID: "r1", Kind: session.KindAssistantText, Text: reply, TurnID: &turn, CreatedAt: at(20), UpdatedAt: at(20)},
			{ID: "p2", Kind: session.KindUserPrompt, Text: "Check Settings too.", TurnID: &turn2, CreatedAt: at(30)},
			{ID: "c2", Kind: session.KindToolCall, TurnID: &turn2, CreatedAt: at(32), UpdatedAt: at(32),
				Tool: &session.ToolCall{CallID: "c2", ToolName: "Edit", Status: "pending", Input: in(map[string]any{"file_path": "/repo/internal/ui/kit/clickable.go"}), Diff: diff}},
		},
	}, true
}

// pickSample finds a pixel inside bounds (clamped to clip) whose topmost
// semantic node is wantID (or any descendant of wantID).
func pickSample(r *input.Router, n input.SemanticNode, clip image.Rectangle) (f32.Point, bool) {
	b := n.Desc.Bounds.Intersect(clip)
	if b.Empty() {
		return f32.Point{}, false
	}
	owns := map[input.SemanticID]bool{}
	var mark func(input.SemanticNode)
	mark = func(cur input.SemanticNode) {
		owns[cur.ID] = true
		for _, ch := range cur.Children {
			mark(ch)
		}
	}
	mark(n)
	pts := []image.Point{
		image.Pt((b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2),
		image.Pt(b.Min.X+min(4, max(0, b.Dx()-1)), (b.Min.Y+b.Max.Y)/2),
		image.Pt(b.Max.X-1-min(4, max(0, b.Dx()-1)), (b.Min.Y+b.Max.Y)/2),
		image.Pt((b.Min.X+b.Max.X)/2, b.Min.Y+min(4, max(0, b.Dy()-1))),
		image.Pt((b.Min.X+b.Max.X)/2, b.Max.Y-1-min(4, max(0, b.Dy()-1))),
	}
	for _, pt := range pts {
		p := f32.Pt(float32(pt.X)+0.5, float32(pt.Y)+0.5)
		if id, ok := r.SemanticAt(p); ok && owns[id] {
			return p, true
		}
	}
	return f32.Point{}, false
}

// checkClickables walks the semantic tree of the last rendered frame,
// collects every enabled ClickGesture node inside region, and moves the
// pointer over each one via frame() to check that Gio's input.Router reports
// pointer.CursorPointer (or pointer.CursorText for editors and selectable
// text). allowDefault may exempt full-surface background focus catchers.
func checkClickables(t *testing.T, r *input.Router, frame func(), region image.Rectangle, allowDefault func(input.SemanticNode) bool) (pointers, texts int) {
	t.Helper()
	type sample struct {
		node input.SemanticNode
		pt   f32.Point
	}
	var samples []sample
	var walk func(nodes []input.SemanticNode)
	walk = func(nodes []input.SemanticNode) {
		for _, n := range nodes {
			walk(n.Children)
			if n.Desc.Disabled {
				continue
			}
			if n.Desc.Class != semantic.Editor && (n.Desc.Gestures&input.ClickGesture == 0 || n.Desc.Gestures&input.ScrollGesture != 0) {
				continue
			}
			b := n.Desc.Bounds.Intersect(region)
			if b.Empty() {
				continue
			}
			pt, ok := pickSample(r, n, region)
			if !ok {
				continue
			}
			samples = append(samples, sample{node: n, pt: pt})
		}
	}
	walk(r.AppendSemantics(nil))
	for _, s := range samples {
		r.Queue(pointer.Event{Kind: pointer.Move, Position: s.pt, Source: pointer.Mouse})
		frame()
		got := r.Cursor()
		if s.node.Desc.Class == semantic.Editor {
			if got != pointer.CursorText {
				t.Errorf("editor %q at %v: cursor %v, want CursorText", s.node.Desc.Label, s.node.Desc.Bounds, got)
			}
			texts++
			continue
		}
		if got == pointer.CursorText {
			texts++
			continue
		}
		if got == pointer.CursorPointer {
			pointers++
			continue
		}
		if got == pointer.CursorGrab && s.node.Desc.Label == "Drag to reorder" { // a queued message's grip
			continue
		}
		if allowDefault != nil && allowDefault(s.node) {
			continue
		}
		t.Errorf("clickable area %q (class=%v bounds=%v) at %v: cursor %v, want CursorPointer", s.node.Desc.Label, s.node.Desc.Class, s.node.Desc.Bounds.Intersect(region), s.pt, got)
	}
	return pointers, texts
}

func newPointerUI(t *testing.T) (*ui, *FakeBackend) {
	t.Helper()
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	fb := NewFakeBackend()
	fb.st.Workspaces = append(fb.st.Workspaces, model.Workspace{
		ID: "wc", SessionID: "s1", Name: "chat", Label: "Pointer cursor",
		Path: t.TempDir(), Chat: "c1", Branch: "pointer-cursor",
	})
	s1 := &fb.st.Sessions[slices.IndexFunc(fb.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
	s1.Order = append(s1.Order, "wc")
	fb.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateRunning, Title: "Pointer cursor"}}
	fb.st.Tasks = []model.Task{{ID: "tk1", SessionID: "s1", GroupID: "p1", Dir: t.TempDir(), Cmd: []string{"claude", "Queued background task"}}}
	b := pointerChatBackend{FakeBackend: fb}
	cfg, _ := config.LoadFile(filepath.Join(t.TempDir(), "config.toml"))
	u := &ui{b: b, th: theme.Dark(), cfg: cfg, panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Font.UISize = 13
	u.cfg.Chat.Claude = true
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "wc", "")
	return u, fb
}

// TestPointerCursor renders frames of the chat, the sidebar, Settings and
// split panes and checks that every enabled clickable area sets
// pointer.CursorPointer, selectable text and editors set pointer.CursorText,
// and split dividers keep their resize cursors.
func TestPointerCursor(t *testing.T) {
	size := image.Pt(1280, 900)
	step := func(u *ui, r *input.Router, ops *op.Ops, now *time.Time) func() {
		return func() {
			ops.Reset()
			*now = now.Add(time.Second)
			u.layout(gl.Context{Ops: ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: *now})
			r.Frame(ops)
		}
	}

	t.Run("SidebarAndChat", func(t *testing.T) {
		u, fb := newPointerUI(t)
		st := fb.State()
		cmp := u.composer(&st, "c1")
		cmp.SetText("Check every clickable in chat")

		var r input.Router
		var ops op.Ops
		now := time.Now()
		frame := step(u, &r, &ops, &now)
		for range 3 {
			frame()
		}

		// Sidebar region (left column) must have zero non-pointer clickables.
		sbW := int(sidebarWidth)
		sbRect := image.Rect(0, 0, sbW, size.Y)
		sbPtr, _ := checkClickables(t, &r, frame, sbRect, nil)
		if sbPtr < 8 {
			t.Fatalf("sidebar clickable count = %d, want at least 8", sbPtr)
		}

		// Chat region (header, transcript, minimap, approval card, composer).
		// Reset pointer away from sidebar hover cards before sampling chat.
		r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(sbW+2), 2), Source: pointer.Mouse})
		frame()
		chatRect := image.Rect(sbRect.Max.X, 0, size.X, size.Y)
		chatPtr, chatText := checkClickables(t, &r, frame, chatRect, func(n input.SemanticNode) bool {
			// Full-transcript background press-catcher (&t.focus / &t.span):
			// spans almost the entire chat area rather than a control.
			b := n.Desc.Bounds.Intersect(chatRect)
			return n.Desc.Label == "" && b.Dx() > chatRect.Dx()*3/4 && b.Dy() > chatRect.Dy()/2
		})
		if chatPtr < 10 {
			t.Fatalf("chat clickable count = %d, want at least 10", chatPtr)
		}
		if chatText < 1 {
			t.Fatalf("chat editor text-cursor count = %d, want at least 1", chatText)
		}

		// Selectable reply prose in the transcript shows CursorText even though
		// its Selectable runs with a disabled Source (so it has no ClickGesture).
		foundProseText := false
		for y := chatRect.Min.Y + 60; y < chatRect.Max.Y-260 && !foundProseText; y += 10 {
			for x := chatRect.Min.X + 40; x < chatRect.Max.X-80; x += 24 {
				r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(x), float32(y)), Source: pointer.Mouse})
				frame()
				if r.Cursor() == pointer.CursorText {
					foundProseText = true
					break
				}
			}
		}
		if !foundProseText {
			t.Error("expected selectable transcript prose to show CursorText")
		}

		// Empty idle composer's Send button is disabled and keeps the default cursor.
		b := u.b.(pointerChatBackend)
		fb.mu.Lock()
		fb.st.Chats[0].State = session.StateReady
		fb.mu.Unlock()
		cmp.SetText("")
		u.b = idleChatBackend{b}
		frame()
		frame()
		var sendPt *f32.Point
		var findSend func([]input.SemanticNode)
		findSend = func(nodes []input.SemanticNode) {
			for _, n := range nodes {
				findSend(n.Children)
				if n.Desc.Label == "Send" && n.Desc.Disabled {
					c := n.Desc.Bounds
					p := f32.Pt(float32(c.Min.X+c.Dx()/2), float32(c.Min.Y+c.Dy()/2))
					sendPt = &p
				}
			}
		}
		findSend(r.AppendSemantics(nil))
		if sendPt == nil {
			t.Fatal("expected disabled Send button in empty idle composer")
		}
		r.Queue(pointer.Event{Kind: pointer.Move, Position: *sendPt, Source: pointer.Mouse})
		frame()
		if got := r.Cursor(); got != pointer.CursorDefault {
			t.Errorf("disabled Send cursor = %v, want CursorDefault", got)
		}
	})

	t.Run("Settings", func(t *testing.T) {
		u, _ := newPointerUI(t)
		u.cfg.Path = filepath.Join(t.TempDir(), "config.toml")
		u.openSettings()

		var r input.Router
		var ops op.Ops
		now := time.Now()
		frame := step(u, &r, &ops, &now)
		for range 3 {
			frame()
		}
		if !u.settings.Shown() {
			t.Fatal("settings closed before cursor check")
		}

		// Settings occupies the pane area right of the sidebar: Appearance first
		// (theme cards, font dropdowns, steppers, nav, Close), then Keyboard
		// shortcuts (keycaps) and Terminal (switches, segmented controls).
		setRect := image.Rect(int(sidebarWidth), 0, size.X, size.Y)
		ptr, txt := checkClickables(t, &r, frame, setRect, nil)
		if ptr < 10 {
			t.Fatalf("settings Appearance clickable count = %d, want at least 10", ptr)
		}
		if txt < 1 {
			t.Fatalf("settings editor count = %d, want at least 1 (search box)", txt)
		}
		clickNav := func(name string, y float32) {
			pt := f32.Pt(float32(setRect.Min.X+80), y)
			r.Queue(pointer.Event{Kind: pointer.Move, Position: pt, Source: pointer.Mouse})
			frame()
			r.Queue(pointer.Event{Kind: pointer.Press, Position: pt, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
			frame()
			r.Queue(pointer.Event{Kind: pointer.Release, Position: pt, Source: pointer.Mouse})
			frame()
			frame()
			if !u.settings.Shown() {
				t.Fatalf("settings closed after clicking %s", name)
			}
			n, _ := checkClickables(t, &r, frame, setRect, nil)
			if n < 10 {
				t.Fatalf("settings %s clickable count = %d, want at least 10", name, n)
			}
		}
		clickNav("Keyboard shortcuts", 160)
		clickNav("Terminal", 196)
	})

	t.Run("MissingCursorFails", func(t *testing.T) {
		// Sanity check: a raw widget.Clickable without kit.Clickable or
		// pointer.CursorPointer reports CursorDefault, not CursorPointer.
		var c widget.Clickable
		var r input.Router
		var ops op.Ops
		sz := image.Pt(80, 28)
		frame := func() {
			ops.Reset()
			gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(sz)}
			c.Layout(gtx, func(gl.Context) gl.Dimensions { return gl.Dimensions{Size: sz} })
			r.Frame(&ops)
		}
		frame()
		r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(40, 14), Source: pointer.Mouse})
		frame()
		if got := r.Cursor(); got != pointer.CursorDefault {
			t.Fatalf("raw widget.Clickable cursor = %v, want CursorDefault", got)
		}
	})

	t.Run("SplitHandles", func(t *testing.T) {
		fb := NewFakeBackend()
		// w1 has a horizontal split (two columns -> vertical divider with CursorColResize)
		// and a nested vertical split (two rows -> horizontal divider with CursorRowResize).
		i := slices.IndexFunc(fb.st.Workspaces, func(w model.Workspace) bool { return w.ID == "w1" })
		fb.st.Workspaces[i].Tabs[0].Layout = &layout.Node{
			Dir:    layout.Horizontal,
			Ratios: []float64{0.5, 0.5},
			Children: []*layout.Node{
				{Pane: "a"},
				{Dir: layout.Vertical, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{
					{Pane: "b"},
					{Pane: "c"},
				}},
			},
		}
		u := &ui{b: fb, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		st := fb.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "w1", "")

		var r input.Router
		var ops op.Ops
		now := time.Now()
		frame := step(u, &r, &ops, &now)
		for range 3 {
			frame()
		}

		pa, pb, pc := u.panes["a"].rect, u.panes["b"].rect, u.panes["c"].rect
		colDiv := f32.Pt(float32(pa.Max.X+pb.Min.X)/2, float32(pa.Min.Y+pa.Dy()/2))
		r.Queue(pointer.Event{Kind: pointer.Move, Position: colDiv, Source: pointer.Mouse})
		frame()
		if got := r.Cursor(); got != pointer.CursorColResize {
			t.Errorf("column split handle cursor = %v, want CursorColResize", got)
		}
		rowDiv := f32.Pt(float32(pb.Min.X+pb.Dx()/2), float32(pb.Max.Y+pc.Min.Y)/2)
		r.Queue(pointer.Event{Kind: pointer.Move, Position: rowDiv, Source: pointer.Mouse})
		frame()
		if got := r.Cursor(); got != pointer.CursorRowResize {
			t.Errorf("row split handle cursor = %v, want CursorRowResize", got)
		}
		termPt := f32.Pt(float32(pa.Min.X+pa.Dx()/2), float32(pa.Min.Y+pa.Dy()/2))
		r.Queue(pointer.Event{Kind: pointer.Move, Position: termPt, Source: pointer.Mouse})
		frame()
		if got := r.Cursor(); got != pointer.CursorText {
			t.Errorf("terminal pane cursor = %v, want CursorText", got)
		}
	})
}

type idleChatBackend struct{ pointerChatBackend }

func (b idleChatBackend) Chat(id string) (session.Snapshot, bool) {
	s, ok := b.pointerChatBackend.Chat(id)
	s.Running = false
	s.SessionState = session.StateReady
	s.PendingApprovals = nil
	s.QueuedInputs = nil
	return s, ok
}

// TestPointerCursorShots renders offscreen PNGs of the chat + sidebar and
// Settings to $PITWALL_CURSOR_SHOTS for visual review; it skips without it.
func TestPointerCursorShots(t *testing.T) {
	dir := os.Getenv("PITWALL_CURSOR_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_CURSOR_SHOTS is not set")
	}
	size := image.Pt(1280, 900)
	shot := func(name string, u *ui, hover f32.Point) {
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		var r input.Router
		var ops op.Ops
		now := time.Now()
		for i := range 4 {
			ops.Reset()
			now = now.Add(time.Second)
			if i >= 2 && hover != (f32.Point{}) {
				r.Queue(pointer.Event{Kind: pointer.Move, Position: hover, Source: pointer.Mouse})
			}
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: now})
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
	}

	u1, fb1 := newPointerUI(t)
	st := fb1.State()
	u1.composer(&st, "c1").SetText("Check every clickable in chat")
	shot("chat-sidebar", u1, f32.Pt(100, 120))

	u2, _ := newPointerUI(t)
	u2.cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	u2.openSettings()
	shot("settings", u2, f32.Pt(400, 160))
}
