package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// maxRewindSteps caps the rows paneRewind walks up the rewind picker.
const maxRewindSteps = 400

// rewindRestore is the confirm menu's option that rewinds the conversation
// and leaves the files alone, as ChatRewind's KeepFiles.
const rewindRestore = "Restore conversation"

// paneRewind rewinds pane chat pc to before the prompt that started turn
// m.Turn, through Claude Code's own picker: /rewind, up to the prompt,
// Restore conversation. The files stay as they are. Claude Code puts the
// prompt back in its input box; that is cleared, since the window's
// composer takes it instead. The chat drops the prompt and what came
// after it at once; the transcript's next prompt branches off before it.
func (d *Daemon) paneRewind(pc *paneChat, m proto.ChatRewind) error {
	switch {
	case pc.prov != model.ProviderClaude:
		return chatError{"unsupported", "only a Claude Code pane chat rewinds; rewind Codex in its terminal"}
	case !m.KeepFiles:
		return chatError{"unsupported", "a Claude Code pane rewinds the conversation only; its files stay as they are"}
	}
	pc.mu.Lock()
	k := rewindTarget(pc.snap.Content, m.Turn)
	var text, id string
	var prompts []string // the target's text and the later prompts'
	if k >= 0 {
		text, id = pc.snap.Content[k].Text, pc.snap.Content[k].ID
		for _, it := range pc.snap.Content[k:] {
			if it.Kind == session.KindUserPrompt {
				prompts = append(prompts, it.Text)
			}
		}
	}
	pc.mu.Unlock()
	if k < 0 {
		return invalid("no prompt starts turn %s", m.Turn)
	}
	defer d.paneCommandLock(pc.id)()
	p, since, err := d.typeCommand(pc.id, "/rewind")
	if err != nil {
		return err
	}
	keys := menuKeys{d, pc.id, p, since}
	picker := func(g vt.Grid) bool { _, ok := agent.RewindPick(g); return ok }
	label, ok := waitLabel(p, "")
	if !ok {
		if picker(p.Snapshot()) {
			return keys.cancelWith(picker, errors.New("Claude Code's rewind list did not read, so nothing changed"))
		}
		return errors.New("Claude Code's rewind list did not open, so nothing changed")
	}
	lost := errors.New("Claude Code's rewind list does not show that message, so nothing changed")
	for step := 0; ; step++ {
		// The newest row that reads as the target's is the target's only
		// when no later prompt reads the same: a label cut with "…", a
		// repeated prompt, or one sent mid-turn, which the picker may not
		// list. Rather than guess, the user picks in the terminal.
		if agent.RewindMatch(label, text) {
			if rewindRank(prompts, label) != 1 {
				return keys.cancelWith(picker, chatError{"conflict", "Claude Code's rewind list shows this message like a later one, so nothing changed; rewind in the terminal (Alt+T) to pick it yourself"})
			}
			break
		}
		if step == maxRewindSteps {
			return keys.cancelWith(picker, lost)
		}
		if err := keys.press(picker, key.NameUpArrow); err != nil {
			return err
		}
		from := label
		if label, ok = waitLabel(p, from); !ok || label == from || label == agent.RewindCurrent {
			return keys.cancelWith(picker, lost) // the top, or it wrapped around
		}
	}
	if err := keys.press(picker, key.NameReturn); err != nil {
		return err
	}
	confirm := func(g vt.Grid) bool { _, _, ok := agent.RewindConfirm(g); return ok }
	if err := keys.chooseRewind(confirm); err != nil {
		return err
	}
	// Claude Code types the prompt back into its box; clear it, as a draft
	// there would hold the composer's next send.
	for deadline := time.Now().Add(menuWait.get()); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if g := p.Snapshot(); !confirm(g) && agent.Draft(model.ProviderClaude, g) {
			draft := func(g vt.Grid) bool { return agent.Draft(model.ProviderClaude, g) }
			if err := keys.press(draft, key.NameEscape); err != nil {
				return err
			}
			if err := keys.press(draft, key.NameEscape); err != nil {
				return err
			}
			break
		}
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if k := slices.IndexFunc(pc.snap.Content, func(it session.Item) bool { return it.ID == id }); k >= 0 {
		pc.cut(k)
	}
	return nil
}

// chooseRewind moves the confirm menu's cursor to rewindRestore and
// presses Enter.
func (m menuKeys) chooseRewind(confirm func(vt.Grid) bool) error {
	read := func() (int, int, bool) {
		opts, at, ok := agent.RewindConfirm(m.p.Snapshot())
		return slices.Index(opts, rewindRestore), at, ok
	}
	target, at, ok := waitMenu(read, func(_, _ int, ok bool) bool { return ok })
	switch {
	case !ok:
		return errors.New("Claude Code did not ask how to rewind; check its tab")
	case target < 0:
		return m.cancelWith(confirm, errors.New("Claude Code offers no "+rewindRestore+" here, so nothing changed"))
	}
	for range maxMenuSteps {
		if at == target {
			return m.press(confirm, key.NameReturn)
		}
		step := key.NameDownArrow
		if target < at {
			step = key.NameUpArrow
		}
		if err := m.press(confirm, step); err != nil {
			return err
		}
		from := at
		if target, at, ok = waitMenu(read, func(_, at int, ok bool) bool { return ok && at != from }); !ok || at == from {
			break
		}
	}
	return m.cancelWith(confirm, errors.New("Claude Code's rewind menu did not move, so nothing changed"))
}

// cancelWith closes the menu up reads with Esc and returns err.
func (m menuKeys) cancelWith(up func(vt.Grid) bool, err error) error {
	if e := m.press(up, key.NameEscape); e != nil {
		return e
	}
	return err
}

// waitLabel reads the rewind picker's cursor row until it is not from or
// menuWait passes.
func waitLabel(p Pane, from string) (string, bool) {
	l, ok := "", false
	for deadline := time.Now().Add(menuWait.get()); ; time.Sleep(20 * time.Millisecond) {
		if l, ok = agent.RewindPick(p.Snapshot()); ok && l != from || time.Now().After(deadline) {
			return l, ok
		}
	}
}

// rewindTarget is the prompt in content that starts turn, by the turn's
// id or the prompt's own; -1 for none.
func rewindTarget(content []session.Item, turn string) int {
	return slices.IndexFunc(content, func(it session.Item) bool {
		return it.Kind == session.KindUserPrompt && turn != "" && (value(it.TurnID) == turn || it.ID == turn)
	})
}

// rewindRank is how many of prompts, the target's text and those of the
// prompts after it, read as picker row label: 1 when the newest row with
// that label is the target's.
func rewindRank(prompts []string, label string) int {
	n := 0
	for _, p := range prompts {
		if agent.RewindMatch(label, p) {
			n++
		}
	}
	return n
}

// cut drops item k and every item after it, and the turns they were in.
// Callers hold pc.mu.
func (pc *paneChat) cut(k int) {
	s := pc.snap
	gone := map[string]bool{}
	for _, it := range s.Content[k:] {
		if t := value(it.TurnID); t != "" {
			gone[t] = true
		}
	}
	s.Content = slices.Clone(s.Content[:k])
	s.Turns = slices.DeleteFunc(slices.Clone(s.Turns), func(t session.TurnSummary) bool { return gone[t.ID] })
	pc.commit([]session.Delta{{Type: session.DeltaSnapshot, Snapshot: &s}}, session.Stamp(time.Now()))
}

// chainLine is where a transcript line sits in the conversation.
type chainLine struct {
	UUID      string `json:"uuid"`
	Parent    string `json:"parentUuid"`
	Sidechain bool   `json:"isSidechain"`
}

// branched notes transcript line, which made es, in the conversation, and
// when it is a prompt that branches off before the tip, as the first one
// after a rewind does, drops the items after its parent's line: the
// prompts the rewind left behind and what they started. Callers hold
// pc.mu.
func (pc *paneChat) branched(line []byte, es []session.Event) {
	var l chainLine
	if json.Unmarshal(line, &l) != nil || l.UUID == "" || l.Sidechain {
		return
	}
	prompt := slices.ContainsFunc(es, func(e session.Event) bool {
		return e.Type == session.EventContent && e.Kind == session.KindUserPrompt
	})
	if prompt {
		pc.cutAfter(l.Parent)
	}
	if pc.lineAt == nil {
		pc.lineAt = map[string]int64{}
	}
	pc.lineAt[l.UUID], pc.tip = pc.cur, l.UUID
}

// cutAfter drops what was read after line parent, which the conversation
// goes on from, when that is not the last line read and a prompt is among
// what goes: the turns a rewind left in the file. A parent from before
// everything read leaves nothing read on the conversation, as when the
// tail starts after it. Callers hold pc.mu.
func (pc *paneChat) cutAfter(parent string) {
	if parent == "" || parent == pc.tip || pc.tip == "" {
		return
	}
	k := 0
	if off, ok := pc.lineAt[parent]; ok {
		k = slices.IndexFunc(pc.at, func(at int64) bool { return at > off })
	}
	if k >= 0 && k < len(pc.snap.Content) && slices.ContainsFunc(pc.snap.Content[k:], func(it session.Item) bool {
		return it.Kind == session.KindUserPrompt
	}) {
		pc.cut(k)
	}
}

// nextParent is the parent of the first conversation line at or after
// offset at in f when that line is a prompt, as one that branched off is;
// "" for any other line, or none within a few whole lines.
func (pc *paneChat) nextParent(f io.ReaderAt, at int64) string {
	r := bufio.NewReader(io.NewSectionReader(f, at, math.MaxInt64-at))
	for range 64 {
		line, err := r.ReadBytes('\n')
		var l chainLine
		if bytes.HasSuffix(line, []byte("\n")) && json.Unmarshal(line, &l) == nil && l.UUID != "" && !l.Sidechain {
			if slices.ContainsFunc(pc.transcript().Line(line), func(e session.Event) bool {
				return e.Type == session.EventContent && e.Kind == session.KindUserPrompt
			}) {
				return l.Parent
			}
			return ""
		}
		if err != nil {
			return ""
		}
	}
	return ""
}
