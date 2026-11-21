package daemon

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/session"
)

// An agent writes a message to its transcript only once the message is
// whole: Claude Code a text block when it ends, Codex its message, agy a
// step, while the TUI draws each as it streams. Measured on Claude Code
// 2.1.330, Codex 0.171 and agy 1.5.14, a 300-word reply showed on screen
// 5 to 9 s before its transcript line. So a pane chat shows what the
// screen has ahead of the transcript in its snapshot's Live, and drops
// each part once the transcript's own item for it lands.

// liveIDs are the ids of a pane chat's Live items, one of each kind.
const (
	liveReply   = "live-reply"
	liveThought = "live-thought"
	liveCall    = "live-call"
	livePrompt  = "live-prompt"
)

// echoFor is how long the prompt a chat sent shows before its transcript
// line, at most: a prompt the agent took as something else never lands.
const echoFor = 15 * time.Second

// liveGrace is how long a reply block shows after its turn ended while
// the transcript lacks it.
const liveGrace = 3 * time.Second

// landedReach is how many of the transcript's last items landed reads.
const landedReach = 50

// foldMatch is how many folded characters of a screen's block and a
// transcript item must agree for the item to be the block's.
const foldMatch = 60

// liveState is what a pane chat read off its agent's screen ahead of the
// transcript (live) and the prompt it sent that the transcript lacks.
type liveState struct {
	turn  string         // the turn rows are of
	ended time.Time      // when turn ended, zero while it runs
	rows  []string       // the reply block from its first row, as last read
	items []session.Item // the screen's part of Live, as last read
	echo  *session.Item  // the prompt sent, until the transcript has it
	after string         // the transcript's last prompt when echo was sent
	at    time.Time      // when echo was sent
}

// live reads what pane l's screen shows of the running turn that the
// transcript lacks, and puts it with the prompt the chat sent in the
// snapshot's Live. Callers hold pc.mu.
func (pc *paneChat) live(l paneLive) {
	pc.lv.items = nil
	turn := runningTurn(pc.snap)
	switch {
	case turn != "" && pc.tr.Running():
		if turn != pc.lv.turn {
			pc.lv.rows = nil
		}
		pc.lv.turn, pc.lv.ended = turn, time.Time{}
	case pc.lv.turn != "" && len(pc.lv.rows) > 0:
		// why: Claude Code ends a turn on its Stop hook before it writes the turn's last text block.
		if pc.lv.ended.IsZero() {
			pc.lv.ended = time.Now()
		}
		if time.Since(pc.lv.ended) < liveGrace {
			turn = pc.lv.turn
		}
	}
	if turn != "" && l.p != nil {
		lv := agent.LiveScreen(pc.prov, l.p.Snapshot())
		// why: Codex can draw its reply's last rows over again from further up; the rows read so far stay until the screen catches up.
		if rows := stitch(pc.lv.rows, lv); rows != nil {
			pc.lv.rows = rows
		}
		pc.lv.items = pc.liveItems(turn, lv)
	} else {
		pc.lv.rows, pc.lv.turn = nil, ""
	}
	if e := pc.lv.echo; e != nil && (lastPrompt(pc.snap) != pc.lv.after || time.Since(pc.lv.at) > echoFor) {
		pc.lv.echo = nil
	}
	pc.setLive()
}

// liveItems are the parts of lv, read off the screen in turn, that the
// transcript does not have yet: the reply block, and agy's running
// command and thought. Callers hold pc.mu.
func (pc *paneChat) liveItems(turn string, lv agent.Live) []session.Item {
	var out []session.Item
	add := func(id string, kind session.ItemKind, text string, tool *session.ToolCall) {
		p := pc.prov
		out = append(out, session.Item{ID: id, Kind: kind, Text: text, Tool: tool, TurnID: &turn, Provider: &p})
	}
	if text := agent.Unwrap(pc.lv.rows, lv.Width); text != "" && !pc.landed(session.KindAssistantText, text) {
		add(liveReply, session.KindAssistantText, text, nil)
	}
	if c := lv.Command; c != "" && !pc.called(turn, c) {
		in, _ := json.Marshal(map[string]string{"command": c})
		add(liveCall, session.KindToolCall, "", &session.ToolCall{CallID: liveCall, ToolName: "Bash", Command: c, Input: in, Status: "running"})
	}
	if t := lv.Thinking; t != "" && !pc.landed(session.KindReasoningText, t) {
		add(liveThought, session.KindReasoningText, t, nil)
	}
	return out
}

// landed reports whether the transcript has text's item: one of kind,
// among its last landedReach items, whose folded start is text's. A turn
// just begun still has the last turn's reply on screen until the TUI
// draws the prompt, so the items before the running turn count too.
func (pc *paneChat) landed(kind session.ItemKind, text string) bool {
	f := agent.Fold(text, foldMatch)
	c := pc.snap.Content
	for i := len(c) - 1; i >= max(0, len(c)-landedReach); i-- {
		if c[i].Kind != kind {
			continue
		}
		g := agent.Fold(c[i].Text, foldMatch)
		if n := min(len(f), len(g)); n > 0 && f[:n] == g[:n] {
			return true
		}
	}
	return false
}

// called reports whether the transcript's last item in turn is a call of
// command: agy writes a call once it ends, as its screen moves on.
func (pc *paneChat) called(turn, command string) bool {
	c := pc.snap.Content
	if len(c) == 0 {
		return false
	}
	it := c[len(c)-1]
	return it.Tool != nil && it.Tool.Command == command && it.TurnID != nil && *it.TurnID == turn
}

// stitch is the reply block lv shows, from its first row: lv's rows when
// they start there, else rows, the block as read before, up to where
// lv's rows overlap it by two text rows or more, then lv's. Blank rows
// do not count toward the overlap: Codex draws the blank row between two
// paragraphs only once the second has rows of its own. It is nil for a
// screen with no block and for one whose block starts off the screen and
// overlaps nothing read before, which the chat cannot place.
func stitch(rows []string, lv agent.Live) []string {
	if lv.Top || len(lv.Rows) == 0 {
		return lv.Rows
	}
	if len(rows) == 0 {
		return nil
	}
	known, seen := texts(rows), texts(lv.Rows)
	same := func(a, b int) bool {
		if a == len(rows)-1 { // the row the agent was writing may have grown since
			return strings.HasPrefix(lv.Rows[b], rows[a])
		}
		return rows[a] == lv.Rows[b]
	}
	for i := range known {
		n := len(known) - i
		if n < 2 || n > len(seen) {
			continue
		}
		if slices.EqualFunc(known[i:], seen[:n], same) {
			return append(slices.Clone(rows[:known[i]]), lv.Rows[seen[0]:]...)
		}
	}
	return nil
}

// texts are the indexes of rows that are not blank.
func texts(rows []string) []int {
	var out []int
	for i, r := range rows {
		if strings.TrimSpace(r) != "" {
			out = append(out, i)
		}
	}
	return out
}

// runningTurn is the id of s's running turn, "" for none.
func runningTurn(s session.Snapshot) string {
	for i := len(s.Turns) - 1; i >= 0; i-- {
		if s.Turns[i].State == session.TurnRunning {
			return s.Turns[i].ID
		}
	}
	return ""
}

// lastPrompt is the id of s's last user prompt, "" for none.
func lastPrompt(s session.Snapshot) string {
	for i := len(s.Content) - 1; i >= 0; i-- {
		if s.Content[i].Kind == session.KindUserPrompt {
			return s.Content[i].ID
		}
	}
	return ""
}

// echoPrompt shows text, a prompt the chat types into the pane, in Live
// until the transcript's line for it lands. A command or a shell line
// writes no prompt line, so it shows none. Callers hold pc.mu.
func (pc *paneChat) echoPrompt(text string) {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!") {
		return
	}
	p := pc.prov
	pc.lv.echo = &session.Item{ID: livePrompt, Kind: session.KindUserPrompt, Text: text, Provider: &p, CreatedAt: session.Stamp(time.Now())}
	pc.lv.after, pc.lv.at = lastPrompt(pc.snap), time.Now()
	pc.setLive()
}

// unecho drops the prompt echoPrompt showed, as when typing it failed.
// Callers hold pc.mu.
func (pc *paneChat) unecho() {
	pc.lv.echo = nil
	pc.setLive()
}

// setLive puts the screen's part and the prompt sent in the snapshot's
// Live when they changed. Callers hold pc.mu.
func (pc *paneChat) setLive() {
	items := slices.Clone(pc.lv.items)
	if e := pc.lv.echo; e != nil {
		items = append(items, *e)
	}
	if slices.EqualFunc(items, pc.snap.Live, sameLive) {
		return
	}
	pc.commit([]session.Delta{{Type: session.DeltaLive, Live: items}}, session.Stamp(time.Now()))
}

// sameLive reports whether two Live items show the same.
func sameLive(a, b session.Item) bool {
	if a.ID != b.ID || a.Text != b.Text || (a.Tool == nil) != (b.Tool == nil) || a.TurnID == nil != (b.TurnID == nil) {
		return false
	}
	return (a.Tool == nil || a.Tool.Command == b.Tool.Command) && (a.TurnID == nil || *a.TurnID == *b.TurnID)
}
