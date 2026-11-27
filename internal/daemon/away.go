package daemon

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// awayState is when the user last saw each chat; the Since you left
// cards that show are State.Away. A chat shows while a window has it on
// screen focused (client.focus, from SeePane), or while a phone has it
// open in the foreground: a stream on it (phone) and a view beat within
// phoneViewTTL (beat), which the app and the page send while the screen
// is on and stop when they leave the foreground. d.mu guards it.
type awayState struct {
	seen  map[string]time.Time   // chat: when it last stopped showing, or the user typed in it
	on    map[string]bool        // chat: it shows now
	phone map[string]int         // chat: the phone streams that have it open
	beat  map[string]time.Time   // chat: a phone's last beat that it shows the chat
	timer map[string]*time.Timer // chat: ends the phone's sight of it phoneViewTTL after its beat
}

// phoneViewTTL is how long a phone's view beat counts: the app and the
// page beat every phoneBeat while they show the chat, so a phone whose
// screen went off, or whose app the OS froze, stops counting by then.
// Tests shorten it.
var phoneViewTTL = 75 * time.Second

// phoneOn reports whether a phone shows chat id at now.
func (a *awayState) phoneOn(id string, now time.Time) bool {
	b, ok := a.beat[id]
	return a.phone[id] > 0 && ok && now.Sub(b) < phoneViewTTL
}

// The summary's caps: the user's last message from its start, the
// agent's words since from their end, as Flockbook sends them.
const (
	awayUserRunes  = 1500
	awayReplyRunes = 6000
	awayLineRunes  = 200 // a longer summary line is cut
	awayFiles      = 4   // edited files a card names
)

// awaySummarize runs prompt as a one-off headless run of run's agent and
// returns what it printed; tests replace it.
var awaySummarize = func(ctx context.Context, run suggestRun, prompt string) (string, error) {
	cmd, _ := suggestCommand(run.prov, run.env, prompt)
	return runSuggest(ctx, run, cmd)
}

// awayLook notes whether chat id shows now, and when the user comes back
// to it after model.AwayAfter or more, opens its card. Callers hold d.mu.
func (d *Daemon) awayLook(id string) { d.awayLookAt(id, time.Time{}) }

// awayLookAt is awayLook for a chat that, if it no longer shows, was last
// seen at left: a phone's last beat. Zero left is now.
func (d *Daemon) awayLookAt(id string, left time.Time) {
	if id == "" {
		return
	}
	a := &d.away
	now := time.Now()
	term := d.terminalOf(id)
	on := d.shownFocused(id) || term != "" && d.shownFocused(term) || a.phoneOn(id, now)
	if on == a.on[id] {
		return
	}
	if a.on == nil {
		a.on, a.seen = map[string]bool{}, map[string]time.Time{}
	}
	since, ok := a.seen[id]
	a.seen[id] = now
	if !on {
		if !left.IsZero() {
			a.seen[id] = left
		}
		delete(a.on, id)
		return
	}
	a.on[id] = true
	if ok && now.Sub(since) >= model.AwayAfter {
		if c, ok := d.st.Away[id]; ok {
			since = c.Since // a card not dismissed yet: it grows to cover both absences
		}
		go d.awayOpen(id, since, now)
	}
}

// awaySeen notes the user typed or sent in chat id: they see it, and its
// card goes. Typing in the pane of a chat tab's TUI counts for its chat.
// Callers hold d.mu.
func (d *Daemon) awaySeen(id string) {
	for _, id := range []string{id, d.awayChatOf(id)} {
		if id == "" {
			continue
		}
		if d.away.seen != nil {
			d.away.seen[id] = time.Now()
		}
		d.awayDrop(id)
	}
}

// awayLooks is awayLook for pane or chat id and, for the pane of a chat
// tab's TUI, its chat. Callers hold d.mu.
func (d *Daemon) awayLooks(id string) {
	d.awayLook(id)
	if c := d.awayChatOf(id); c != "" {
		d.awayLook(c)
	}
}

// awayChatOf is the chat whose tab shows its TUI in pane, "" for none.
// Callers hold d.mu.
func (d *Daemon) awayChatOf(pane string) string {
	if c := d.chatOfTerminal(pane); c != nil {
		return c.id
	}
	return ""
}

// terminalOf is the pane of chat id's TUI while its tab shows it
// (proto.ChatTerminal), "" otherwise. Callers hold d.mu.
func (d *Daemon) terminalOf(id string) string {
	if i := slices.IndexFunc(d.st.Chats, func(c model.Chat) bool { return c.ID == id }); i >= 0 {
		return d.st.Chats[i].Terminal
	}
	return ""
}

// awayDrop removes chat id's card. Callers hold d.mu.
func (d *Daemon) awayDrop(id string) {
	if _, ok := d.st.Away[id]; ok {
		delete(d.st.Away, id)
		d.changed()
	}
}

// awayPhone counts a phone stream opening (+1) or closing (-1) chat id.
func (d *Daemon) awayPhone(id string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.away.phone == nil {
		d.away.phone = map[string]int{}
	}
	if d.away.phone[id] += n; d.away.phone[id] <= 0 {
		delete(d.away.phone, id)
		d.awayUnbeat(id)
	}
	d.awayLook(id)
}

// awayView notes a phone's beat that it shows chat id now (on), or that
// it left the foreground or the chat (!on).
func (d *Daemon) awayView(id string, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !on {
		d.awayUnbeat(id)
		d.awayLook(id)
		return
	}
	a := &d.away
	if a.beat == nil {
		a.beat, a.timer = map[string]time.Time{}, map[string]*time.Timer{}
	}
	now := time.Now()
	a.beat[id] = now
	if t := a.timer[id]; t != nil {
		t.Stop()
	}
	a.timer[id] = time.AfterFunc(phoneViewTTL, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if a.beat[id].Equal(now) {
			d.awayLookAt(id, now) // the phone went quiet: it last saw the chat at its beat
		}
	})
	d.awayLook(id)
}

// awayUnbeat forgets the phone's beat for chat id. Callers hold d.mu.
func (d *Daemon) awayUnbeat(id string) {
	if t := d.away.timer[id]; t != nil {
		t.Stop()
	}
	delete(d.away.timer, id)
	delete(d.away.beat, id)
}

// awayOpen builds chat id's card for a return at at after the user last
// saw it at since, shows its facts, then with [chat] away_summary on asks
// the agent's cheapest model for the summary, once.
func (d *Daemon) awayOpen(id string, since, at time.Time) {
	// A chat tab showing its agent's TUI keeps its chat closed meanwhile;
	// the TUI's pane reads the same session as a pane chat does.
	src := id
	d.mu.Lock()
	if t := d.terminalOf(id); t != "" {
		src = t
	}
	d.mu.Unlock()
	d.awayFresh(src)
	run, err := d.suggestOf(src, 1<<30)
	if err != nil {
		return
	}
	card, ok := awayFacts(run.snap, since)
	if !ok {
		return // the agent did nothing meanwhile
	}
	card.Chat, card.At = id, at
	_, headless := suggestCommand(run.prov, run.env, "")
	prompt := awayPrompt(run.snap, since)
	card.Asking = d.o.AwaySummary != nil && d.o.AwaySummary() && headless && !run.guarded && prompt != ""
	d.mu.Lock()
	if d.st.Away == nil {
		d.st.Away = map[string]model.Away{}
	}
	d.st.Away[id] = card
	d.changed()
	d.mu.Unlock()
	if !card.Asking {
		return
	}
	run.busy = func() bool { return false } // a summary of what happened stays true while the agent works on
	out, err := awaySummarize(context.Background(), run, prompt)
	status, needs := awayLines(out)
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.st.Away[id]
	if !ok || !c.At.Equal(at) {
		return // dismissed, or a newer return replaced it
	}
	c.Asking = false
	if err == nil {
		c.Status, c.Needs = status, needs
	}
	d.st.Away[id] = c
	d.changed()
}

// awayFresh reads what pane chat id's transcript gained while nothing
// streamed it, as for an agent the user watches in its terminal, so the
// card's facts are current; a chat tab's chat is always current.
func (d *Daemon) awayFresh(id string) {
	pc, l, ok := d.paneChatOf(id)
	if !ok {
		return
	}
	d.mu.Lock()
	streamed := d.paneStreaming(id)
	d.mu.Unlock()
	if streamed {
		return
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	_ = pc.catchUp(l) // why: a transcript that cannot be read leaves the chat as it was
}

// itemAt is when item it began, zero when it does not say.
func itemAt(it session.Item) time.Time {
	t, err := time.Parse(time.RFC3339Nano, it.CreatedAt)
	if err != nil {
		t, _ = time.Parse(time.RFC3339Nano, it.UpdatedAt)
	}
	return t
}

// awayEdits says whether a tool of this name edits files, as the chat's
// Changes panel counts them.
func awayEdits(tool string) bool {
	switch tool {
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "edit", "write", "write_file", "replace", "apply_patch":
		return true
	}
	return false
}

// testRun matches a command line that runs tests.
var testRun = regexp.MustCompile(`(^|[\s/;&|])(go test|cargo test|pytest|jest|vitest|mix test|rspec|phpunit|ctest|(npm|pnpm|yarn|bun)( run)? test|make (test|check))\b|_test\.sh\b`)

// awayFacts is the card for snap's items since since: how many messages
// and turns, the files edited, the commands, the last failed one and the
// last test run; false when the agent did nothing in that time. Seq is
// the first missed item's place as the phone numbers entries
// (phoneEntries).
func awayFacts(snap session.Snapshot, since time.Time) (model.Away, bool) {
	var a model.Away
	turns, files := map[string]bool{}, map[string]bool{}
	agent := false
	for i, it := range snap.Content {
		if at := itemAt(it); at.IsZero() || !at.After(since) {
			continue
		}
		if a.Item == "" {
			a.Item, a.Seq = it.ID, int64(i+1)
		}
		if it.TurnID != nil && *it.TurnID != "" {
			turns[*it.TurnID] = true
		}
		switch it.Kind {
		case session.KindAssistantText:
			if strings.TrimSpace(it.Text) != "" {
				a.Replies++
				agent = true
			}
		case session.KindToolCall:
			t := it.Tool
			if t == nil {
				continue
			}
			agent = true
			if awayEdits(t.ToolName) && t.Status != "failed" {
				paths := []string{flow.ToolPath(*t)}
				if len(t.Files) > 0 {
					paths = paths[:0]
					for _, f := range t.Files {
						paths = append(paths, f.Path)
					}
				}
				for _, p := range paths {
					if p != "" && !files[p] {
						files[p] = true
						a.Edited = append(a.Edited, logs.Redact(filepath.Base(p)))
					}
				}
			}
			if t.Command == "" || t.Status == "running" {
				continue
			}
			a.Commands++
			failed := t.Status == "failed" || t.ExitCode != nil && *t.ExitCode != 0
			line, _, _ := strings.Cut(strings.TrimSpace(t.Command), "\n")
			if failed {
				a.Failed = logs.Redact(cutRunes(line, 120))
			}
			if testRun.MatchString(line) {
				a.Tests = "passed"
				if failed {
					a.Tests = "failed"
				}
			}
		}
	}
	if !agent {
		return model.Away{}, false
	}
	a.Since, a.Turns, a.EditedN = since, len(turns), len(a.Edited)
	a.Edited = a.Edited[max(0, len(a.Edited)-awayFiles):]
	return a, true
}

// awayPrompt is the summary's request: the user's newest message, its
// first awayUserRunes, and what the agent said since since, its last
// awayReplyRunes, both redacted; "" when the agent said nothing.
func awayPrompt(snap session.Snapshot, since time.Time) string {
	var user string
	var said []string
	for _, it := range snap.Content {
		switch it.Kind {
		case session.KindUserPrompt:
			user = it.Text
		case session.KindAssistantText:
			if at := itemAt(it); !at.IsZero() && at.After(since) && strings.TrimSpace(it.Text) != "" {
				said = append(said, strings.TrimSpace(it.Text))
			}
		}
	}
	if len(said) == 0 {
		return ""
	}
	reply := []rune(logs.Redact(strings.Join(said, "\n\n")))
	if len(reply) > awayReplyRunes {
		reply = append([]rune{'…'}, reply[len(reply)-awayReplyRunes:]...) // its end says where the work stands
	}
	return "The user of a coding agent stepped away and just came back. Here is their last message to the agent and what the agent said while they were away.\n\n" +
		"<user_message>\n" + cutRunes(logs.Redact(strings.TrimSpace(user)), awayUserRunes) + "\n</user_message>\n\n" +
		"<agent_replies>\n" + string(reply) + "\n</agent_replies>\n\n" +
		"Reply with exactly two lines and nothing else, each under 20 words. Do not use tools.\n" +
		"State: where the work stands now.\n" +
		"Needs: what the agent needs from the user now, or Nothing."
}

// awayLines are the summary's two lines from the model's answer: its
// State: and Needs: lines, else its first two lines.
func awayLines(out string) (status, needs string) {
	var rest []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.Trim(strings.TrimSpace(line), "*`\"")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		v = strings.Trim(v, "*` ")
		switch strings.ToLower(strings.Trim(k, "*` ")) {
		case "state":
			if ok && status == "" {
				status = v
				continue
			}
		case "needs":
			if ok && needs == "" {
				needs = v
				continue
			}
		}
		rest = append(rest, line)
	}
	if status == "" && needs == "" && len(rest) > 0 {
		status = rest[0]
		if len(rest) > 1 {
			needs = rest[1]
		}
	}
	return cutRunes(logs.Redact(status), awayLineRunes), cutRunes(logs.Redact(needs), awayLineRunes)
}

// cutRunes is s cut to n runes, with an ellipsis when it was longer.
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
