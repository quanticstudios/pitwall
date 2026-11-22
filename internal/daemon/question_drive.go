package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// questionPane is the slice of Pane question driving reads and writes.
type questionPane interface {
	Snapshot() vt.Grid
	Modes() vt.Modes
}

// liveAsk tracks a TUI pane chat's live question screen so the desktop card
// and the terminal stay on the same step across Claude Code, Codex and
// Antigravity.
type liveAsk struct {
	id        string
	prov      model.Provider
	questions []session.Question
	step      int
	review    bool
	done      []bool
	answered  map[string][]string
	synthetic bool // opened from the TUI screen rather than the transcript
	unans     int  // Codex's last seen unanswered count, -1 when unknown
}

// syncQuestion keeps pc.snap.PendingUserInputs synchronized with q, the
// active question read from the pane's TUI screen. Callers hold pc.mu.
func (pc *paneChat) syncQuestion(l paneLive, q *agent.Question) {
	if q == nil {
		if pc.ask != nil && pc.ask.synthetic {
			id := pc.ask.id
			pc.ask = nil
			pc.reduce([]session.Event{{Type: session.EventInputResolved, RequestID: id}})
		} else {
			pc.ask = nil
		}
		return
	}

	// If the transcript already opened a rich InputRequest (Claude Code's
	// AskUserQuestion with full headers, descriptions and previews), enrich it
	// with the TUI's live step, tab checkmarks and multi-select ticks.
	if n := len(pc.snap.PendingUserInputs); n > 0 && (pc.ask == nil || !pc.ask.synthetic) {
		in := pc.snap.PendingUserInputs[n-1]
		if pc.ask == nil || pc.ask.id != in.ID {
			pc.ask = &liveAsk{
				id:        in.ID,
				prov:      pc.prov,
				questions: slices.Clone(in.Questions),
				done:      make([]bool, len(in.Questions)),
				answered:  map[string][]string{},
				review:    pc.prov == model.ProviderClaude && (len(in.Questions) > 1 || (len(in.Questions) == 1 && in.Questions[0].Multi)),
				unans:     -1,
			}
		}
		step := stepQuestionIndex(pc.prov, q, in.Questions)
		if step < 0 {
			step = pc.ask.step
		}
		pc.ask.step = step
		if len(q.Tabs) == len(in.Questions) {
			for i, tab := range q.Tabs {
				pc.ask.done[i] = tab.Answered
			}
		}
		syncLiveAnswers(pc.ask, q, step)
		updated := in
		st := step
		updated.Step = &st
		updated.Review = pc.ask.review
		updated.Done = slices.Clone(pc.ask.done)
		updated.Answered = cloneAnswerMap(pc.ask.answered)
		if !reflect.DeepEqual(in, updated) {
			all := slices.Clone(pc.snap.PendingUserInputs)
			all[n-1] = updated
			set, _ := json.Marshal(map[string]any{"pendingUserInputs": all})
			pc.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
		}
		return
	}

	// Synthetic TUI question: Codex, Antigravity, or Claude before its
	// transcript writes the AskUserQuestion tool call.
	total := max(1, q.Total)
	needNew := pc.ask == nil || !pc.ask.synthetic || len(pc.ask.questions) != total
	if !needNew && len(q.AllPrompts) == total {
		for i, p := range q.AllPrompts {
			if pc.ask.questions[i].Prompt != p {
				needNew = true
				break
			}
		}
	}
	if needNew {
		if pc.ask != nil && pc.ask.synthetic {
			pc.reduce([]session.Event{{Type: session.EventInputResolved, RequestID: pc.ask.id}})
		}
		id := "tui-ask-" + q.ID
		if !l.act.UpdatedAt.IsZero() {
			id = "tui-ask-" + strconv.FormatInt(l.act.UpdatedAt.UnixNano(), 10)
		}
		qs := make([]session.Question, total)
		for i := range qs {
			qs[i] = session.Question{ID: fmt.Sprintf("q%d", i+1), Prompt: fmt.Sprintf("Question %d of %d", i+1, total), Options: []session.PromptOption{}}
			if i < len(q.AllPrompts) {
				qs[i].Prompt = q.AllPrompts[i]
			}
			if i < len(q.Tabs) {
				qs[i].Header = q.Tabs[i].Label
			}
		}
		pc.ask = &liveAsk{
			id:        id,
			prov:      pc.prov,
			questions: qs,
			step:      clampStep(q.Index, total, q.Review),
			review:    pc.prov == model.ProviderClaude && (total > 1 || q.Multi),
			done:      make([]bool, total),
			answered:  map[string][]string{},
			synthetic: true,
			unans:     codexUnanswered(q.Prompt),
		}
	}

	cur := stepQuestionIndex(pc.prov, q, pc.ask.questions)
	if cur < 0 {
		cur = clampStep(q.Index, len(pc.ask.questions), q.Review)
	}
	if cur < len(pc.ask.questions) && !q.Review {
		sq := &pc.ask.questions[cur]
		sq.Prompt = cmp.Or(q.StepPrompt, q.Prompt)
		sq.Multi = q.Multi
		if cur < len(q.Tabs) && q.Tabs[cur].Label != "" {
			sq.Header = q.Tabs[cur].Label
		}
		opts := make([]session.PromptOption, len(q.Options))
		for i, o := range q.Options {
			opts[i] = session.PromptOption{ID: o.Label, Label: o.Label, Description: o.Description}
		}
		sq.Options = opts
	}
	if pc.prov == model.ProviderClaude && len(q.Tabs) == len(pc.ask.done) {
		for i, tab := range q.Tabs {
			pc.ask.done[i] = tab.Answered
		}
	} else if cur != pc.ask.step && pc.ask.step >= 0 && pc.ask.step < len(pc.ask.done) {
		if pc.prov == model.ProviderCodex {
			if u := codexUnanswered(q.Prompt); u >= 0 && pc.ask.unans >= 0 && u < pc.ask.unans {
				pc.ask.done[pc.ask.step] = true
			}
		} else if cur > pc.ask.step {
			pc.ask.done[pc.ask.step] = true
		}
	}
	if u := codexUnanswered(q.Prompt); u >= 0 {
		pc.ask.unans = u
	}
	pc.ask.step = cur
	syncLiveAnswers(pc.ask, q, cur)

	st := cur
	detail := cmp.Or(q.StepPrompt, q.Prompt)
	in := session.InputRequest{
		ID:        pc.ask.id,
		Detail:    &detail,
		Questions: cloneQuestions(pc.ask.questions),
		Step:      &st,
		Review:    pc.ask.review,
		Done:      slices.Clone(pc.ask.done),
		Answered:  cloneAnswerMap(pc.ask.answered),
	}
	if idx := slices.IndexFunc(pc.snap.PendingUserInputs, func(x session.InputRequest) bool { return x.ID == in.ID }); idx >= 0 {
		if !reflect.DeepEqual(pc.snap.PendingUserInputs[idx], in) {
			all := slices.Clone(pc.snap.PendingUserInputs)
			all[idx] = in
			set, _ := json.Marshal(map[string]any{"pendingUserInputs": all})
			pc.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
		}
		return
	}
	pc.reduce([]session.Event{{Type: session.EventInputRequested, RequestID: in.ID, Detail: detail, Questions: in.Questions}})
	if idx := slices.IndexFunc(pc.snap.PendingUserInputs, func(x session.InputRequest) bool { return x.ID == in.ID }); idx >= 0 {
		all := slices.Clone(pc.snap.PendingUserInputs)
		all[idx] = in
		set, _ := json.Marshal(map[string]any{"pendingUserInputs": all})
		pc.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
	}
}

func clampStep(idx, total int, review bool) int {
	if review {
		return total
	}
	return max(0, min(idx, total-1))
}

func syncLiveAnswers(a *liveAsk, q *agent.Question, step int) {
	if q.Review {
		for i, sq := range a.questions {
			if raw, ok := q.ReviewAns[sq.Prompt]; ok && raw != "" {
				a.answered[sq.ID] = splitReviewLabels(raw, sq)
				if i < len(a.done) {
					a.done[i] = true
				}
			}
		}
		return
	}
	if q.Multi && step >= 0 && step < len(a.questions) {
		var picked []string
		for _, o := range q.Options {
			if o.Selected {
				picked = append(picked, o.Label)
			}
		}
		sq := a.questions[step]
		// Preserve any custom typed text if already recorded alongside checkboxes.
		for _, prev := range a.answered[sq.ID] {
			if !slices.ContainsFunc(q.Options, func(o agent.QuestionOption) bool { return matchOptionLabel(o.Label, prev) }) {
				picked = append(picked, prev)
			}
		}
		if len(picked) > 0 {
			a.answered[sq.ID] = picked
		} else {
			delete(a.answered, sq.ID)
		}
	}
}

func splitReviewLabels(raw string, sq session.Question) []string {
	if !sq.Multi {
		return []string{raw}
	}
	var out []string
	for rest := raw; rest != ""; {
		hit := ""
		for _, o := range sq.Options {
			if (rest == o.Label || strings.HasPrefix(rest, o.Label+", ")) && len(o.Label) > len(hit) {
				hit = o.Label
			}
		}
		if hit == "" {
			return append(out, rest)
		}
		out = append(out, hit)
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, hit), ", ")
	}
	return out
}

func codexUnanswered(prompt string) int {
	_, rest, ok := strings.Cut(prompt, "(")
	if !ok {
		return -1
	}
	num, _, ok := strings.Cut(rest, " unanswered)")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return -1
	}
	return n
}

func cloneAnswerMap(m map[string][]string) map[string][]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = slices.Clone(v)
	}
	return out
}

func cloneQuestions(qs []session.Question) []session.Question {
	out := make([]session.Question, len(qs))
	for i, q := range qs {
		q.Options = slices.Clone(q.Options)
		out[i] = q
	}
	return out
}

// paneAnswer drives the pane's TUI question screen according to m: a single
// step ("answer", "goto", "submit"), or all questions in sequence when m.Step
// is empty. It checks the screen before and after every step and stops with a
// clear note if the TUI is not on the expected question.
func (d *Daemon) paneAnswer(id string, l paneLive, m proto.ChatAnswer) error {
	pc, _, ok := d.paneChatOf(id)
	if !ok {
		return errResolved
	}
	pc.mu.Lock()
	var in *session.InputRequest
	for i := range pc.snap.PendingUserInputs {
		if pc.snap.PendingUserInputs[i].ID == m.Request {
			cp := pc.snap.PendingUserInputs[i]
			cp.Questions = cloneQuestions(cp.Questions)
			in = &cp
			break
		}
	}
	pc.mu.Unlock()
	if in == nil {
		return errResolved
	}
	if l.p == nil {
		return errors.New("the agent exited")
	}

	var err error
	switch m.Step {
	case "goto":
		err = d.driveGoto(id, l.p, l.prov, in.Questions, m.Target)
	case "submit":
		err = d.driveSubmitReview(id, l.p, l.prov)
	case "answer":
		if m.Target < 0 || m.Target >= len(in.Questions) {
			return invalid("question step %d out of range", m.Target)
		}
		vals := m.Answers[in.Questions[m.Target].ID]
		err = d.driveAnswerStep(id, l.p, l.prov, in.Questions, m.Target, in.Review, vals)
		if err == nil {
			pc.mu.Lock()
			if pc.ask != nil && pc.ask.id == in.ID {
				if pc.ask.answered == nil {
					pc.ask.answered = map[string][]string{}
				}
				pc.ask.answered[in.Questions[m.Target].ID] = slices.Clone(vals)
				if m.Target < len(pc.ask.done) {
					pc.ask.done[m.Target] = true
				}
			}
			pc.mu.Unlock()
		}
	case "":
		err = d.answerAllPages(id, l.p, l.prov, in.Questions, m.Answers)
	default:
		return invalid("unknown question step action %q", m.Step)
	}

	// Sync snapshot immediately so the card reflects the TUI's post-step state
	// (including on mismatch errors, so the card snaps to the TUI's real step).
	pc.mu.Lock()
	pc.catchUp(l)
	pc.mu.Unlock()
	return err
}

// answerPages answers qs on pane id's Claude Code question pages in sequence.
func (d *Daemon) answerPages(id string, p questionPane, qs []session.Question, answers map[string][]string) error {
	return d.answerAllPages(id, p, model.ProviderClaude, qs, answers)
}

// answerAllPages drives every question in qs on pane id for prov, checking
// the screen before and after every step and stopping with a clear note if
// the TUI is not on the expected question.
func (d *Daemon) answerAllPages(id string, p questionPane, prov model.Provider, qs []session.Question, answers map[string][]string) error {
	if len(qs) == 0 {
		return nil
	}
	review := prov == model.ProviderClaude && (len(qs) > 1 || qs[0].Multi)
	for k, sq := range qs {
		q := agent.ReadQuestion(prov, p.Snapshot())
		if q == nil {
			return fmt.Errorf("%s shows no question to answer", provLabel(prov))
		}
		if cur := stepQuestionIndex(prov, q, qs); cur >= 0 && cur != k {
			if err := d.driveGoto(id, p, prov, qs, k); err != nil {
				return err
			}
		}
		if err := d.driveAnswerStep(id, p, prov, qs, k, review, answers[sq.ID]); err != nil {
			return err
		}
	}
	if review {
		q := agent.ReadQuestion(prov, p.Snapshot())
		if q != nil && q.Review {
			return d.driveSubmitReview(id, p, prov)
		}
	}
	return nil
}

// driveGoto moves the TUI from its current question page to target using ←/→,
// verifying the screen moved to the expected step after every arrow key.
func (d *Daemon) driveGoto(id string, p questionPane, prov model.Provider, qs []session.Question, target int) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	maxStep := len(qs) - 1
	if prov == model.ProviderClaude && (len(qs) > 1 || (len(qs) == 1 && qs[0].Multi)) {
		maxStep = len(qs) // Claude's Review/Submit tab
	}
	if target < 0 || target > maxStep {
		return fmt.Errorf("step %d is out of range", target+1)
	}
	q := agent.ReadQuestion(prov, p.Snapshot())
	if q == nil {
		return fmt.Errorf("%s shows no question to navigate", provLabel(prov))
	}
	if prov == model.ProviderCodex && q.Draft {
		return errors.New("there are notes in the terminal; send or clear them first")
	}
	if prov == model.ProviderCodex && q.Notes {
		if err := d.sendPaneKey(id, p, press(key.NameTab, p.Modes())); err != nil {
			return err
		}
		time.Sleep(replyDelay.get())
		q = agent.ReadQuestion(prov, p.Snapshot())
		if q == nil {
			return fmt.Errorf("%s's question closed while leaving notes", provLabel(prov))
		}
	}
	if prov == model.ProviderAntigravity && q.WriteIn {
		if err := d.sendPaneKey(id, p, press(key.NameEscape, p.Modes())); err != nil {
			return err
		}
		q = waitQuestionUntil(prov, p, func(n *agent.Question) bool { return n == nil || !n.WriteIn })
		if q == nil || q.WriteIn {
			return errors.New("Antigravity did not leave the write-in box")
		}
	}
	cur := stepQuestionIndex(prov, q, qs)
	if cur < 0 {
		return fmt.Errorf("%s is on an unexpected question (%q); answer it in the terminal", provLabel(prov), cmp.Or(q.StepPrompt, q.Prompt))
	}
	for steps := 0; cur != target && steps < len(qs)+2; steps++ {
		dir := key.NameRightArrow
		want := cur + 1
		if target < cur {
			dir = key.NameLeftArrow
			want = cur - 1
		}
		if err := d.sendPaneKey(id, p, press(dir, p.Modes())); err != nil {
			return err
		}
		q = waitQuestionUntil(prov, p, func(n *agent.Question) bool {
			return n == nil || stepQuestionIndex(prov, n, qs) != cur
		})
		if q == nil {
			return fmt.Errorf("%s's question closed while moving to step %d", provLabel(prov), want+1)
		}
		next := stepQuestionIndex(prov, q, qs)
		if next != want {
			return fmt.Errorf("%s moved to %q instead of step %d; stopped without guessing", provLabel(prov), cmp.Or(q.StepPrompt, q.Prompt), want+1)
		}
		cur = next
	}
	if cur != target {
		return fmt.Errorf("could not reach step %d in %s", target+1, provLabel(prov))
	}
	return nil
}

// driveAnswerStep verifies the TUI is on step, sends the CLI key sequence for
// vals, and verifies the TUI moved to the expected next step (or closed).
func (d *Daemon) driveAnswerStep(id string, p questionPane, prov model.Provider, qs []session.Question, step int, review bool, vals []string) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	if len(vals) == 0 {
		return errors.New("pick or type an answer first")
	}
	q := agent.ReadQuestion(prov, p.Snapshot())
	if err := verifyQuestionStep(prov, q, qs, step); err != nil {
		return err
	}
	if prov == model.ProviderCodex && q.Draft {
		return errors.New("there are notes in the terminal; send or clear them first")
	}
	if prov == model.ProviderCodex && q.Notes {
		if err := d.sendPaneKey(id, p, press(key.NameTab, p.Modes())); err != nil {
			return err
		}
		time.Sleep(replyDelay.get())
		q = agent.ReadQuestion(prov, p.Snapshot())
		if err := verifyQuestionStep(prov, q, qs, step); err != nil {
			return err
		}
	}
	if prov == model.ProviderAntigravity && q.WriteIn {
		oldID := q.ID
		if err := d.sendPaneKey(id, p, press(key.NameEscape, p.Modes())); err != nil {
			return err
		}
		q = waitQuestionAfter(prov, p, oldID, true)
		if err := verifyQuestionStep(prov, q, qs, step); err != nil {
			return err
		}
	}

	var lastID string
	var err error
	if q.Multi {
		lastID, err = d.driveMultiStep(id, p, prov, qs, step, q, vals)
	} else {
		lastID, err = d.driveSingleStep(id, p, prov, q, vals[0])
	}
	if err != nil {
		return err
	}

	// Verify the screen moved to the expected next step or resolved.
	expectMore := step < len(qs)-1 || review
	after := waitQuestionAfterStep(prov, p, lastID, qs, step, expectMore)
	return verifyAfterStep(prov, after, qs, step, review)
}

func (d *Daemon) driveSingleStep(id string, p questionPane, prov model.Provider, q *agent.Question, val string) (string, error) {
	idx := findOptionIndex(q.Options, val)
	if idx >= 0 {
		if err := d.moveCursor(id, p, q.Cursor, idx); err != nil {
			return q.ID, err
		}
		if cur := agent.ReadQuestion(prov, p.Snapshot()); cur != nil {
			q = cur
		}
		return q.ID, d.sendPaneKey(id, p, press(key.NameReturn, p.Modes()))
	}
	// Custom / Other answer.
	if !q.Other || q.OtherIndex < 0 {
		return q.ID, fmt.Errorf("%q is not an option and this question does not accept a custom answer", val)
	}
	clean := remote.Clean(val)
	if clean == "" {
		return q.ID, errors.New("type an answer for Other")
	}
	if err := d.moveCursor(id, p, q.Cursor, q.OtherIndex); err != nil {
		return q.ID, err
	}
	switch prov {
	case model.ProviderCodex:
		if err := d.sendPaneKey(id, p, press(key.NameTab, p.Modes())); err != nil {
			return q.ID, err
		}
		time.Sleep(replyDelay.get())
	case model.ProviderAntigravity:
		if err := d.sendPaneKey(id, p, press(key.NameReturn, p.Modes())); err != nil {
			return q.ID, err
		}
		open := waitQuestionUntil(prov, p, func(n *agent.Question) bool { return n == nil || n.WriteIn })
		if open == nil || !open.WriteIn {
			return q.ID, errors.New("Antigravity did not open its write-in input")
		}
		q = open
	default:
		// Claude Code focuses "Type something" when cursor moves onto it.
		time.Sleep(replyDelay.get())
		if q.Draft {
			kill := input.Key(key.Event{Name: "U", Modifiers: key.ModCtrl, State: key.Press}, p.Modes())
			if err := d.sendPaneKey(id, p, kill); err != nil {
				return q.ID, err
			}
			time.Sleep(replyDelay.get())
		}
	}
	if err := d.sendPaneKey(id, p, input.Paste(clean, p.Modes())); err != nil {
		return q.ID, err
	}
	time.Sleep(replyDelay.get())
	if cur := agent.ReadQuestion(prov, p.Snapshot()); cur != nil {
		q = cur
	}
	return q.ID, d.sendPaneKey(id, p, press(key.NameReturn, p.Modes()))
}

func (d *Daemon) driveMultiStep(id string, p questionPane, prov model.Provider, qs []session.Question, step int, q *agent.Question, vals []string) (string, error) {
	want := make([]bool, len(q.Options))
	var otherText string
	for _, v := range vals {
		if idx := findOptionIndex(q.Options, v); idx >= 0 {
			want[idx] = true
		} else if clean := remote.Clean(v); clean != "" {
			otherText = clean
		}
	}
	for i := range q.Options {
		if want[i] == q.Options[i].Selected {
			continue
		}
		if err := d.moveCursor(id, p, q.Cursor, i); err != nil {
			return q.ID, err
		}
		if err := d.sendPaneKey(id, p, press(key.NameSpace, p.Modes())); err != nil {
			return q.ID, err
		}
		q = waitQuestionUntil(prov, p, func(n *agent.Question) bool {
			return n == nil || !questionMatchesStep(prov, n, qs, step) || (i < len(n.Options) && n.Options[i].Selected == want[i])
		})
		if err := verifyQuestionStep(prov, q, qs, step); err != nil {
			return "", fmt.Errorf("multi-select toggle stopped: %w", err)
		}
		if i >= len(q.Options) || q.Options[i].Selected != want[i] {
			return q.ID, fmt.Errorf("%s did not toggle %q; stopped without guessing", provLabel(prov), q.Options[i].Label)
		}
	}
	if otherText != "" {
		if !q.Other || q.OtherIndex < 0 {
			return q.ID, fmt.Errorf("%q is not an option and this question does not accept a custom answer", otherText)
		}
		if err := d.moveCursor(id, p, q.Cursor, q.OtherIndex); err != nil {
			return q.ID, err
		}
		if prov == model.ProviderAntigravity {
			if err := d.sendPaneKey(id, p, press(key.NameReturn, p.Modes())); err != nil {
				return q.ID, err
			}
			open := waitQuestionUntil(prov, p, func(n *agent.Question) bool { return n == nil || n.WriteIn })
			if open == nil || !open.WriteIn {
				return q.ID, errors.New("Antigravity did not open its write-in input")
			}
			if err := d.sendPaneKey(id, p, input.Paste(otherText, p.Modes())); err != nil {
				return open.ID, err
			}
			time.Sleep(replyDelay.get())
			if cur := agent.ReadQuestion(prov, p.Snapshot()); cur != nil {
				open = cur
			}
			// In Antigravity, Enter inside Write-in submits this step directly.
			return open.ID, d.sendPaneKey(id, p, press(key.NameReturn, p.Modes()))
		}
		// Claude Code multi-select Other: typing on "Type something" fills it,
		// Enter confirms it on the same step (or advances if Next is below).
		time.Sleep(replyDelay.get())
		if err := d.sendPaneKey(id, p, input.Paste(otherText, p.Modes())); err != nil {
			return q.ID, err
		}
		time.Sleep(replyDelay.get())
		oldID := q.ID
		if cur := agent.ReadQuestion(prov, p.Snapshot()); cur != nil {
			oldID = cur.ID
		}
		if err := d.sendPaneKey(id, p, press(key.NameReturn, p.Modes())); err != nil {
			return oldID, err
		}
		next := waitQuestionAfter(prov, p, oldID, true)
		if next == nil || !questionMatchesStep(prov, next, qs, step) {
			return oldID, nil
		}
		q = next
	}
	// Advance off the multi-select step.
	switch prov {
	case model.ProviderAntigravity:
		if q.Cursor == q.OtherIndex && len(q.Options) > 0 {
			if err := d.moveCursor(id, p, q.Cursor, 0); err != nil {
				return q.ID, err
			}
		}
		if cur := agent.ReadQuestion(prov, p.Snapshot()); cur != nil {
			q = cur
		}
		return q.ID, d.sendPaneKey(id, p, press(key.NameReturn, p.Modes()))
	default:
		if q.Submit && q.SubmitIndex >= 0 {
			if err := d.moveCursor(id, p, q.Cursor, q.SubmitIndex); err != nil {
				return q.ID, err
			}
			if cur := agent.ReadQuestion(prov, p.Snapshot()); cur != nil {
				q = cur
			}
			return q.ID, d.sendPaneKey(id, p, press(key.NameReturn, p.Modes()))
		}
		if q.CanNext {
			return q.ID, d.sendPaneKey(id, p, press(key.NameRightArrow, p.Modes()))
		}
		return q.ID, errors.New("cannot advance past this multi-select question")
	}
}

// driveSubmitReview verifies the TUI is on Claude Code's Review/Submit screen
// and presses Enter on "Submit answers".
func (d *Daemon) driveSubmitReview(id string, p questionPane, prov model.Provider) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	q := agent.ReadQuestion(prov, p.Snapshot())
	if q == nil || !q.Review || len(q.Options) == 0 || q.Options[0].Label != "Submit answers" {
		return fmt.Errorf("%s is not on the Review your answers screen; stopped without guessing", provLabel(prov))
	}
	if err := d.moveCursor(id, p, q.Cursor, 0); err != nil {
		return err
	}
	if err := d.sendPaneKey(id, p, press(key.NameReturn, p.Modes())); err != nil {
		return err
	}
	if after := waitQuestionUntil(prov, p, func(n *agent.Question) bool { return n == nil }); after != nil {
		return fmt.Errorf("%s did not submit the answers (still showing %q)", provLabel(prov), cmp.Or(after.StepPrompt, after.Prompt))
	}
	return nil
}

func waitQuestionUntil(prov model.Provider, p questionPane, ok func(*agent.Question) bool) *agent.Question {
	deadline := time.Now().Add(menuWait.get())
	for {
		n := agent.ReadQuestion(prov, p.Snapshot())
		if ok(n) || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *Daemon) moveCursor(id string, p questionPane, from, to int) error {
	dir := key.NameDownArrow
	if to < from {
		dir = key.NameUpArrow
	}
	for i := from; i != to; {
		if err := d.sendPaneKey(id, p, press(dir, p.Modes())); err != nil {
			return err
		}
		if to < i {
			i--
		} else {
			i++
		}
	}
	return nil
}

func (d *Daemon) sendPaneKey(id string, _ questionPane, data []byte) error {
	d.noteUserKey(id, nil)
	return d.handle(context.Background(), proto.Input{Pane: id, Data: data})
}

func waitQuestionAfter(prov model.Provider, p questionPane, oldID string, expectMore bool) *agent.Question {
	deadline := time.Now().Add(menuWait.get())
	for {
		n := agent.ReadQuestion(prov, p.Snapshot())
		if n != nil && n.ID != oldID || n == nil && !expectMore || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitQuestionAfterStep waits until the TUI leaves step (either moving to a
// different step/review or closing when !expectMore).
func waitQuestionAfterStep(prov model.Provider, p questionPane, oldID string, qs []session.Question, step int, expectMore bool) *agent.Question {
	deadline := time.Now().Add(menuWait.get())
	for {
		n := agent.ReadQuestion(prov, p.Snapshot())
		if n == nil {
			if !expectMore {
				return nil
			}
		} else if n.ID != oldID && !questionMatchesStep(prov, n, qs, step) {
			return n
		}
		if time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func verifyQuestionStep(prov model.Provider, q *agent.Question, qs []session.Question, step int) error {
	if q == nil {
		return fmt.Errorf("%s shows no question to answer", provLabel(prov))
	}
	if questionMatchesStep(prov, q, qs, step) {
		return nil
	}
	shown := cmp.Or(q.StepPrompt, q.Prompt)
	want := qs[step].Prompt
	return fmt.Errorf("%s is on %q, not %q; stopped without guessing", provLabel(prov), shown, want)
}

func verifyAfterStep(prov model.Provider, after *agent.Question, qs []session.Question, step int, review bool) error {
	if step < len(qs)-1 {
		if after == nil {
			return fmt.Errorf("%s's question closed unexpectedly after step %d", provLabel(prov), step+1)
		}
		// Allow landing on step+1, or on Claude's Review page when answering an
		// earlier step after later steps were already answered.
		if questionMatchesStep(prov, after, qs, step+1) || (review && after.Review) {
			return nil
		}
		return fmt.Errorf("%s moved to %q instead of %q; stopped without guessing", provLabel(prov), cmp.Or(after.StepPrompt, after.Prompt), qs[step+1].Prompt)
	}
	if review {
		if after == nil {
			// Some single-question multi-select variants may submit directly.
			return nil
		}
		if after.Review {
			return nil
		}
		return fmt.Errorf("%s is on %q instead of Review your answers; stopped without guessing", provLabel(prov), cmp.Or(after.StepPrompt, after.Prompt))
	}
	if after != nil {
		// On Codex/Antigravity, if an earlier question was still unanswered, the
		// TUI may stay open on an unanswered question rather than submitting.
		if next := stepQuestionIndex(prov, after, qs); next >= 0 && next != step {
			return nil
		}
		return fmt.Errorf("%s did not advance past %q", provLabel(prov), cmp.Or(after.StepPrompt, after.Prompt))
	}
	return nil
}

func stepQuestionIndex(prov model.Provider, q *agent.Question, qs []session.Question) int {
	if q == nil {
		return -1
	}
	if q.Review {
		return len(qs)
	}
	if prov == model.ProviderClaude {
		want := collapseWS(cmp.Or(q.StepPrompt, q.Prompt))
		for i, sq := range qs {
			if collapseWS(sq.Prompt) == want {
				return i
			}
		}
		if len(qs) == 1 {
			return 0
		}
		return -1
	}
	if q.Index >= 0 && q.Index < len(qs) {
		return q.Index
	}
	return -1
}

func questionMatchesStep(prov model.Provider, q *agent.Question, qs []session.Question, step int) bool {
	if q == nil || step < 0 || step >= len(qs) || q.Review {
		return false
	}
	if prov == model.ProviderClaude {
		return collapseWS(cmp.Or(q.StepPrompt, q.Prompt)) == collapseWS(qs[step].Prompt)
	}
	if q.Index != step {
		return false
	}
	if p := qs[step].Prompt; p != "" && !strings.HasPrefix(p, "Question ") {
		return collapseWS(cmp.Or(q.StepPrompt, q.Prompt)) == collapseWS(p)
	}
	return true
}

func findOptionIndex(opts []agent.QuestionOption, val string) int {
	for i, o := range opts {
		if matchOptionLabel(o.Label, val) {
			return i
		}
	}
	return -1
}

func matchOptionLabel(screenLabel, val string) bool {
	if screenLabel == val {
		return true
	}
	// Antigravity prefixes recommended options with "(Recommended) ".
	if strings.TrimPrefix(screenLabel, "(Recommended) ") == strings.TrimPrefix(val, "(Recommended) ") {
		return true
	}
	return false
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func provLabel(p model.Provider) string {
	switch p {
	case model.ProviderClaude:
		return "Claude Code"
	case model.ProviderCodex:
		return "Codex"
	case model.ProviderAntigravity:
		return "Antigravity"
	default:
		return "the agent"
	}
}

// Keep unused imports check clean.
var _ = maps.Clone[map[string][]string]
