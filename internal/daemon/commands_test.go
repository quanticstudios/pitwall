package daemon

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type commandPane struct {
	*fakePane
	blocks     []vt.CommandBlock
	integrated bool
}

func (p *commandPane) Commands() ([]vt.CommandBlock, bool) { return p.blocks, p.integrated }
func (p *commandPane) HasPromptMarks() bool                { return p.integrated }

func TestCommandReplyAndMissingIntegration(t *testing.T) {
	p := &commandPane{fakePane: &fakePane{}, integrated: true, blocks: []vt.CommandBlock{{Command: "true", Finished: true, StatusKnown: true}}}
	d := &Daemon{panes: map[string]Pane{"p": p}}
	r := d.commands(proto.Commands{Pane: "p", ID: 42})
	if r.ID != 42 || r.Pane != "p" || !r.Integrated || len(r.Blocks) != 1 || r.Blocks[0].Command != "true" {
		t.Fatalf("reply %+v", r)
	}
	r = d.commands(proto.Commands{Pane: "gone", ID: 43})
	if r.ID != 43 || r.Integrated || len(r.Blocks) > 0 {
		t.Fatalf("missing %+v", r)
	}
	p.integrated = false
	err := d.scroll(proto.Scroll{Pane: "p", Prompts: 1})
	if err == nil || !strings.Contains(err.Error(), "docs/usage.md#shell-integration") {
		t.Fatalf("error %v", err)
	}
	if len(d.views) > 0 {
		t.Fatal("missing integration moved view")
	}
}

func TestCommandReplySizeLimit(t *testing.T) {
	p := &commandPane{fakePane: &fakePane{}, integrated: true, blocks: []vt.CommandBlock{{Command: "large", Output: strings.Repeat("x", 16<<20)}, {Command: "true"}}}
	d := &Daemon{panes: map[string]Pane{"p": p}}
	r := d.commands(proto.Commands{Pane: "p"})
	if !r.More || len(r.Blocks) != 1 || r.Blocks[0].Command != "true" {
		t.Fatalf("limit: more %v, blocks %d", r.More, len(r.Blocks))
	}
}

func TestCommandFrameMarks(t *testing.T) {
	p := &commandPane{fakePane: &fakePane{}}
	d := &Daemon{panes: map[string]Pane{"p": p}}
	for _, marked := range []bool{false, true, false} {
		p.integrated = marked
		if f := d.frame("p", p); f.PromptMarks != marked {
			t.Errorf("frame marks %v, want %v", f.PromptMarks, marked)
		}

		for _, live := range []bool{false, true} {
			c := &client{live: map[string]bool{"p": live}}
			if f := d.frameFor(c, "p", p); f.PromptMarks != marked {
				t.Errorf("live %v: frame marks %v, want %v", live, f.PromptMarks, marked)
			}
		}
	}
}
