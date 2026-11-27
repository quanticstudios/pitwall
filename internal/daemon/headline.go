package daemon

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Turn headlines: when a turn finishes, the model that writes Since you
// left's summary (awaySummarize) writes it one line, kept in
// State.Headlines by chat and turn, which the chat's Headlines view shows.
// [chat] away_summary off calls no model for them either.
const (
	headlineItems   = 400 // the chat's newest items a headline reads
	headlineTools   = 20  // tool lines the prompt names, the newest
	headlineUser    = 1500
	headlineReply   = 4000
	headlineRunes   = 90  // a longer line is cut
	headlineTrivial = 300 // a reply shorter than this, with no tools, gets none
	headlineKeep    = 300 // headlines a chat keeps, the newest
)

// headlineFresh is how long after a turn ended it still gets a headline,
// so a chat read again after a restart does not ask for its old turns.
var headlineFresh = 10 * time.Minute

// headlineDelay is how long a pane's Stop hook waits for its transcript
// to land before the headline reads it. Tests shorten it.
var headlineDelay = 2 * time.Second

// headlineSoon is headline for pane after headlineDelay: an agent pane's
// chat, or the chat whose tab shows its TUI there.
func (d *Daemon) headlineSoon(pane string) {
	time.Sleep(headlineDelay)
	d.mu.Lock()
	id := cmp.Or(d.awayChatOf(pane), pane)
	d.mu.Unlock()
	d.headline(id, pane)
}

// headline writes chat id's newest finished turn its headline, read from
// src (id, or the pane of its TUI), once. A trivial turn, a guarded tab,
// summaries off, or a failed or empty answer leave the turn without one.
func (d *Daemon) headline(id, src string) {
	if d.o.AwaySummary == nil || !d.o.AwaySummary() {
		return
	}
	d.awayFresh(src)
	run, err := d.suggestOf(src, headlineItems)
	if err != nil || run.guarded {
		return
	}
	turn := lastTurn(run.snap)
	if turn == "" || !headlineRecent(run.snap.Turns[len(run.snap.Turns)-1], time.Now()) {
		return
	}
	if _, ok := suggestCommand(run.prov, run.env, ""); !ok {
		return
	}
	key := id + "\x00" + turn
	d.mu.Lock()
	had := model.HeadlineOf(d.st.Headlines[id], turn) != ""
	d.mu.Unlock()
	if had {
		return
	}
	if _, busy := d.heads.LoadOrStore(key, true); busy {
		return // the chat's event and the pane's hook both ask
	}
	defer d.heads.Delete(key)
	prompt := headlinePrompt(run.snap, turn)
	if prompt == "" {
		return
	}
	run.busy = func() bool { return false } // the line stays true while the agent works on
	out, err := awaySummarize(context.Background(), run, prompt)
	line := headlineLine(out)
	if err != nil || line == "" {
		return // why: the turn stays as it is, with no headline and no error in the chat
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setHeadline(id, model.Headline{Turn: turn, Text: line})
}

// setHeadline adds h to chat id's headlines, keeping the newest
// headlineKeep and dropping chats that are gone. Callers hold d.mu.
func (d *Daemon) setHeadline(id string, h model.Headline) {
	hs := append(slices.Clone(d.st.Headlines[id]), h)
	hs = hs[max(0, len(hs)-headlineKeep):]
	m := maps.Clone(d.st.Headlines) // replaced whole: snapshots share the old map
	if m == nil {
		m = map[string][]model.Headline{}
	}
	m[id] = hs
	for c := range m {
		live := slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == c }) ||
			slices.ContainsFunc(d.st.Chats, func(ch model.Chat) bool { return ch.ID == c })
		if !live && c != id {
			delete(m, c)
		}
	}
	d.st.Headlines = m
	d.changed()
}

// headlineRecent is whether turn t ended within headlineFresh of now; a
// turn that does not say when counts as recent.
func headlineRecent(t session.TurnSummary, now time.Time) bool {
	if t.CompletedAt == nil {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, *t.CompletedAt)
	return err != nil || now.Sub(at) < headlineFresh
}

// headlinePrompt asks for turn's headline from its prompt, the tools it
// ran and what the agent said, all redacted; "" when the turn is trivial:
// no tools and a reply under headlineTrivial runes.
func headlinePrompt(snap session.Snapshot, turn string) string {
	var user string
	var said, tools []string
	for _, it := range snap.Content {
		if it.TurnID == nil || *it.TurnID != turn {
			continue
		}
		switch it.Kind {
		case session.KindUserPrompt:
			user = it.Text
		case session.KindAssistantText:
			if s := strings.TrimSpace(it.Text); s != "" {
				said = append(said, s)
			}
		case session.KindToolCall:
			if t := it.Tool; t != nil {
				line, _, _ := strings.Cut(strings.TrimSpace(cmp.Or(t.Command, flow.ToolPath(*t))), "\n")
				tools = append(tools, strings.TrimSpace(t.ToolName+" "+cutRunes(line, 120)))
			}
		}
	}
	reply := strings.Join(said, "\n\n")
	if len(tools) == 0 && utf8.RuneCountInString(reply) < headlineTrivial {
		return ""
	}
	r := []rune(logs.Redact(reply))
	if len(r) > headlineReply {
		r = append([]rune{'…'}, r[len(r)-headlineReply:]...) // its end says what came of it
	}
	tools = tools[max(0, len(tools)-headlineTools):]
	return "Here is one turn of a coding agent's work: the user's message, the tools the agent ran, and what it said.\n\n" +
		"<user_message>\n" + cutRunes(logs.Redact(strings.TrimSpace(user)), headlineUser) + "\n</user_message>\n\n" +
		"<tools>\n" + logs.Redact(strings.Join(tools, "\n")) + "\n</tools>\n\n" +
		"<agent_replies>\n" + string(r) + "\n</agent_replies>\n\n" +
		"Reply with one line and nothing else: a headline of what the agent did in this turn. " +
		"Past tense, specific (name the file, number or result), under 70 characters, no period at the end. Do not use tools."
}

// headlineLine is the headline in the model's answer: its first line,
// without a label, list mark, quotes or a final period; "" for none.
func headlineLine(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*#> ")
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.Trim(k, "*` "), "headline") {
			line = v
		}
		line = strings.TrimSuffix(strings.Trim(line, "*\"' "), ".")
		if line != "" {
			return cutRunes(logs.Redact(line), headlineRunes)
		}
	}
	return ""
}
