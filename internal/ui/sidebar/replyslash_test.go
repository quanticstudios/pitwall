package sidebar_test

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

var replyCommands = []session.Command{
	{Name: "compact", Description: "Free up context by summarizing the conversation so far"},
	{Name: "config", Description: "Open settings", Terminal: true},
	{Name: "context", Description: "Show current context usage"},
	{Name: "ux-review", Description: "Review the UI against the design rules", Source: "user"},
	{Name: "ship", Description: "Commit, push and open a pull request", Source: "project"},
}

// slashSheet is a sidebar whose tab w's Claude asks a question, with
// chatin's / menu on its reply sheet listing replyCommands for the
// agent's provider prov.
type slashSheet struct {
	t    *testing.T
	s    sidebar.Sidebar
	menu chatin.SlashMenu
	r    input.Router
	ops  op.Ops
	st   model.State
	prov model.Provider
	asks []string // the tabs and panes SlashCommands was asked for
}

func newSlashSheet(t *testing.T, prov model.Provider) *slashSheet {
	h := &slashSheet{t: t, prov: prov, st: model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "Session"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Provider: prov}},
		Activities: []model.Activity{{PaneID: "p", WorkspaceID: "w", Provider: prov, State: model.StateAwaitingInput, Detail: "Which check should run before the release?"}},
	}}
	h.s.Slash = &h.menu
	h.s.SlashCommands = func(ws, pane string) ([]session.Command, model.Provider) {
		h.asks = append(h.asks, ws+"/"+pane)
		return replyCommands, h.prov
	}
	h.frame()
	h.s.OpenReply("w", "p")
	h.frame()
	h.frame()
	return h
}

func (h *slashSheet) frame() []sidebar.Event {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(288, 700)), Now: time.Now()}
	_, events := h.s.Layout(gtx, theme.Dark(), &h.st, "", "w")
	h.r.Frame(&h.ops)
	return slices.Clone(events)
}

func (h *slashSheet) queue(evs ...event.Event) []sidebar.Event {
	for _, e := range evs {
		h.r.Queue(e)
	}
	return h.frame()
}

// typ types s at the end of the draft.
func (h *slashSheet) typ(s string) {
	n := utf8.RuneCountInString(h.s.ReplyEditor().Text())
	e := n + utf8.RuneCountInString(s)
	h.queue(key.EditEvent{Range: key.Range{Start: n, End: n}, Text: s}, key.SelectionEvent{Start: e, End: e})
	h.frame()
}

func (h *slashSheet) press(k key.Name) []sidebar.Event {
	return h.queue(key.Event{Name: k, State: key.Press})
}

func (h *slashSheet) rows() []string {
	var out []string
	for _, c := range h.menu.Rows() {
		out = append(out, c.Name)
	}
	return out
}

func (h *slashSheet) set(s string) {
	ed := h.s.ReplyEditor()
	ed.SetText("")
	h.frame()
	h.typ(s)
}

// The reply sheet has the chat composer's / menu, with the commands of
// the row's agent: all of them at the draft's start, only skills and
// custom commands mid-draft. Enter and Tab pick rather than send, a pick
// replaces only the /word, Up moves the highlight rather than bringing
// back a reply, and Esc closes the menu and leaves the sheet open.
func TestReplySlashMenu(t *testing.T) {
	h := newSlashSheet(t, model.ProviderClaude)
	ed := h.s.ReplyEditor()
	h.typ("/")
	if got := h.rows(); len(got) != len(replyCommands) {
		t.Fatalf("/ lists %q", got)
	}
	if !slices.Contains(h.asks, "w/p") {
		t.Fatalf("the sheet asked for the commands of %q, want w/p", h.asks)
	}
	h.typ("co")
	if got := h.rows(); !slices.Equal(got, []string{"compact", "config", "context"}) {
		t.Fatalf("/co lists %q", got)
	}
	h.press(key.NameDownArrow)
	h.press(key.NameUpArrow)
	h.press(key.NameDownArrow)
	if ev := h.press(key.NameReturn); len(ev) != 0 || ed.Text() != "/config " || h.menu.Shown() || h.s.Replying() != "w" {
		t.Fatalf("Enter in the menu: events %v, draft %q, menu %v, replying %q", ev, ed.Text(), h.menu.Shown(), h.s.Replying())
	}

	h.set("run /co")
	if got := h.rows(); len(got) != 0 {
		t.Fatalf("mid-draft /co lists built-ins %q", got)
	}
	h.set("run /")
	if got := h.rows(); !slices.Equal(got, []string{"ux-review", "ship"}) {
		t.Fatalf("mid-draft / lists %q", got)
	}
	ed.SetText("run /s now")
	ed.SetCaret(6, 6)
	h.frame()
	if ev := h.press(key.NameTab); len(ev) != 0 || ed.Text() != "run /ship now" {
		t.Fatalf("Tab mid-draft: events %v, draft %q", ev, ed.Text())
	}

	h.set("/ux")
	if h.press(key.NameEscape); h.menu.Shown() || h.s.Replying() != "w" {
		t.Fatalf("Esc: menu %v, replying %q", h.menu.Shown(), h.s.Replying())
	}
	if h.frame(); h.menu.Shown() {
		t.Fatal("the menu came back on the text Esc closed it on")
	}
	if ev := h.press(key.NameReturn); len(ev) != 1 || ev[0].(sidebar.Reply).Text != "/ux" {
		t.Fatalf("Enter with the menu closed sent %v", ev)
	}
}

// Codex names a skill with $, so a /word mid-draft opens no menu for it.
func TestReplySlashCodex(t *testing.T) {
	h := newSlashSheet(t, model.ProviderCodex)
	h.typ("run /s")
	if h.menu.Shown() {
		t.Fatalf("a Codex reply opened the menu mid-draft: %q", h.rows())
	}
	h.set("/s")
	if !h.menu.Shown() {
		t.Fatal("a Codex reply opened no menu at the draft's start")
	}
}

// TestReplySlashShot renders a headless PNG of the reply sheet's / menu
// into $PITWALL_SLASH_SHOTS; it skips when unset.
func TestReplySlashShot(t *testing.T) {
	dir := os.Getenv("PITWALL_SLASH_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SLASH_SHOTS is not set")
	}
	h := newSlashSheet(t, model.ProviderClaude)
	size := image.Pt(480, 420)
	h.s.Window = size // the sheet is wider than the sidebar and takes the window past it
	h.typ("check with /")
	time.Sleep(300 * time.Millisecond) // the sheet's entrance
	h.ops.Reset()
	paint.Fill(&h.ops, theme.Dark().Bg) // the window past the sidebar
	h.s.Layout(layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(288, size.Y)), Now: time.Now()}, theme.Dark(), &h.st, "", "w")
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	if err := win.Frame(&h.ops); err != nil {
		t.Fatal(err)
	}
	out := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "reply-slash.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		t.Fatal(err)
	}
}
