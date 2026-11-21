package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// confirmRig is a transcript with one prompt whose rewind confirm is
// open, laid out frame by frame.
type confirmRig struct {
	tr   Transcript
	s    *session.Snapshot
	ops  op.Ops
	r    input.Router
	now  time.Time
	size image.Point
}

func newConfirmRig(files bool) *confirmRig {
	turn := "t1"
	g := &confirmRig{now: time.Now(), size: image.Pt(900, 640), s: &session.Snapshot{Content: []session.Item{
		{ID: "p", Kind: session.KindUserPrompt, Text: "Rename the parser and add a test", TurnID: &turn,
			Attachments: []session.Attachment{{ID: "image-1", Kind: "image", MimeType: "image/png", Data: "AAAA"}}},
		{ID: "a", Kind: session.KindAssistantText, Text: "Renamed `parse.go` to `lexer.go` and added `lexer_test.go`.", TurnID: &turn},
	}}}
	g.tr.CanRewind, g.tr.TurnRewind, g.tr.FilesRewind = true, true, files
	g.frame()
	g.tr.msg.confirmTurn, g.tr.msg.confirmItem = turn, "p"
	g.tr.openConfirm("p", turn)
	g.frame()
	g.frame()
	return g
}

func (g *confirmRig) frame() {
	g.ops.Reset()
	g.now = g.now.Add(time.Millisecond)
	gtx := layout.Context{Ops: &g.ops, Metric: unit.Metric{PxPerDp: 1.5, PxPerSp: 1.5}, Constraints: layout.Exact(g.size), Now: g.now, Source: g.r.Source()}
	g.tr.Layout(gtx, theme.Dark(), g.s, false)
	g.r.Frame(&g.ops)
}

// A rewind's confirm asks the daemon about the files once it opens, says
// it is checking, then lists them. Revert files too sends ActRewind with
// the files, and Stash once files changed after the agent's last turn;
// Revert and keep changes keeps them. A refusal, an answer for another
// prompt and an old daemon leave Revert files too off.
func TestRewindConfirm(t *testing.T) {
	g := newConfirmRig(true)
	acts := g.tr.Actions()
	if len(acts) != 1 || acts[0].Kind != ActRewindCheck || acts[0].Turn != "t1" || acts[0].Item != "p" {
		t.Fatalf("opening the confirm sent %+v, want one check", acts)
	}
	if g.tr.canRevert() {
		t.Error("Revert files too is on before the daemon answered")
	}
	g.tr.RewindChecked(proto.ChatRewindFiles{Chat: "c", Turn: "t2", Files: []proto.RewindFile{{Path: "x", Status: "modified"}}})
	if g.tr.msg.checked != nil {
		t.Error("took an answer for another turn")
	}
	g.tr.RewindChecked(proto.ChatRewindFiles{Chat: "c", Turn: "t1", Refused: "Codex in tab \"api\" also works in this folder"})
	g.frame()
	g.tr.msg.revert.Click()
	g.frame()
	if g.tr.canRevert() || len(g.tr.Actions()) != 0 || g.tr.msg.confirm == "" {
		t.Error("a refused check still reverts")
	}

	g.tr.RewindChecked(proto.ChatRewindFiles{Chat: "c", Turn: "t1", Files: []proto.RewindFile{{Path: "lexer.go", From: "parse.go", Status: "renamed"}, {Path: "lexer_test.go", Status: "deleted"}}})
	g.frame()
	g.tr.msg.revert.Click()
	g.frame()
	acts = g.tr.Actions()
	if len(acts) != 1 || acts[0].Kind != ActRewind || acts[0].KeepFiles || acts[0].Stash || acts[0].Turn != "t1" ||
		acts[0].Text != "Rename the parser and add a test" || len(acts[0].Attachments) != 1 || g.tr.msg.confirm != "" {
		t.Fatalf("Revert files too sent %+v", acts)
	}

	g = newConfirmRig(true)
	g.tr.Actions()
	g.tr.RewindChecked(proto.ChatRewindFiles{Turn: "t1", Files: []proto.RewindFile{{Path: "a.go", Status: "modified"}, {Path: "notes.md", Status: "deleted"}},
		Unsaved: []proto.RewindFile{{Path: "notes.md", Status: "added"}}})
	g.frame()
	g.tr.msg.revert.Click()
	g.frame()
	if acts := g.tr.Actions(); len(acts) != 1 || !acts[0].Stash || acts[0].KeepFiles {
		t.Fatalf("Stash and revert files sent %+v", acts)
	}

	g = newConfirmRig(false)
	if acts := g.tr.Actions(); len(acts) != 0 {
		t.Errorf("an old daemon's confirm asked %+v", acts)
	}
	g.tr.msg.ok.Click()
	g.frame()
	if acts := g.tr.Actions(); len(acts) != 1 || !acts[0].KeepFiles {
		t.Fatalf("Revert and keep changes sent %+v", acts)
	}
	if !strings.Contains(g.tr.confirmText(), "The files stay as they are") {
		t.Errorf("an old daemon's confirm says %q", g.tr.confirmText())
	}
}

// TestRewindConfirmShots draws the confirm in each state to
// $PITWALL_REWIND_SHOTS; it skips without it.
func TestRewindConfirmShots(t *testing.T) {
	dir := os.Getenv("PITWALL_REWIND_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_REWIND_SHOTS is not set")
	}
	files := []proto.RewindFile{{Path: "internal/parse/lexer.go", From: "internal/parse/parse.go", Status: "renamed"},
		{Path: "internal/parse/lexer_test.go", Status: "deleted"}, {Path: "docs/parser.md", Status: "modified"}, {Path: "go.sum", Status: "added"}}
	for _, tc := range []struct {
		name  string
		files bool
		check *proto.ChatRewindFiles
	}{
		{"rewind-checking.png", true, nil},
		{"rewind-files.png", true, &proto.ChatRewindFiles{Turn: "t1", Files: files}},
		{"rewind-stash.png", true, &proto.ChatRewindFiles{Turn: "t1", Files: append(files, proto.RewindFile{Path: "scratch.txt", Status: "deleted"}),
			Unsaved: []proto.RewindFile{{Path: "scratch.txt", Status: "added"}}}},
		{"rewind-refused.png", true, &proto.ChatRewindFiles{Turn: "t1", Refused: `Codex in tab "api" also works in this folder, and its edits would go back too; close it, or keep the changes`}},
		{"rewind-old-daemon.png", false, nil},
	} {
		g := newConfirmRig(tc.files)
		if tc.check != nil {
			g.tr.RewindChecked(*tc.check)
		}
		g.frame()
		g.frame()
		win, err := headless.NewWindow(g.size.X, g.size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		if err := win.Frame(&g.ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: g.size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, tc.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
