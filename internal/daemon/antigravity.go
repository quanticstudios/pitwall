package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// agyApp is the app name Antigravity CLI gives its OSC 7501 reports.
const (
	agyApp     = "antigravity"
	agyPlanMsg = "Approve plan"
)

// agyProgram makes an Antigravity CLI report pane id's activity where its
// hooks cannot: agy has no hook for a permission prompt or a question, nor
// for the answer to one. Its own OSC 7501 report says each:
//
//	blocked, kind permission    pending-approval (or plan-ready for Approve plan, awaiting-input for folder trust), Detail = its message
//	blocked, kind question      awaiting-input, Detail = the question
//
// and, only while one of those waits, what came after it: working once
// answered, done or error with its message, idle (nothing) once the turn
// was cancelled. Every other state stays with the hooks, or with the
// screen for an agy without them. Sign-in (kind auth) flashes at each start
// and is left out. Callers hold d.mu.
func (d *Daemon) agyProgram(id string, r progstatus.Report) {
	if r.App != agyApp || r.ID != "" {
		return
	}
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if pi < 0 || d.st.Panes[pi].Provider != model.ProviderAntigravity && d.st.Panes[pi].Provider != "" {
		return
	}
	var prev model.AgentState
	if i := d.activityIndex(id); i >= 0 {
		prev = d.st.Activities[i].State
	}
	var state model.AgentState
	msg := agent.Summary(r.Msg)
	switch {
	case r.State == progstatus.Blocked && r.Kind == progstatus.Permission && r.Msg == agyTrustMsg:
		state = model.StateAwaitingInput // Trust or Don't trust (agent.Offers)
	case r.State == progstatus.Blocked && r.Kind == progstatus.Permission && (r.Msg == agyPlanMsg || strings.HasPrefix(r.Msg, agyPlanMsg)):
		state = model.StatePlanReady
	case r.State == progstatus.Blocked && r.Kind == progstatus.Permission:
		state = model.StatePendingApproval
	case r.State == progstatus.Blocked && r.Kind == progstatus.Question:
		state = model.StateAwaitingInput
	case !waits(prev):
		return
	case r.State == progstatus.Working:
		state, msg = model.StateWorking, ""
	case r.State == progstatus.Done:
		state = model.StateCompleted
	case r.State == progstatus.Error:
		state = model.StateError
	case r.State == progstatus.Idle:
		delete(d.live.screenWait, id)
		d.dropActivity(id)
		d.recordExplanation(id, model.ProviderAntigravity, "", "program-status", agent.ScreenTrace{})
		return
	default:
		return
	}
	delete(d.live.screenWait, id)
	d.setActivity(context.Background(), id, model.ProviderAntigravity, state, msg)
	d.recordExplanation(id, model.ProviderAntigravity, state, "program-status", agent.ScreenTrace{})
}

// agyMode is the mode agy's footer shows on g, "" when g shows no footer.
func agyMode(g vt.Grid) string {
	mode, _, _, _ := agent.AgyFooter(g)
	return mode
}

// agyValue is a model id or an effort a pane chat may type after /model
// or /effort: one word, nothing agy's input box would read as more.
var agyValue = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// agySet sets an agy pane chat's model and effort with agy's own /model
// and /effort commands, and its mode with Shift+Tab until the footer
// reads it, as a user would in the TUI.
func (d *Daemon) agySet(id string, p Pane, m proto.ChatSet) error {
	if m.Provider != "" && m.Provider != model.ProviderAntigravity {
		return chatError{"unsupported", "this chat is Antigravity in a pane; it cannot switch provider"}
	}
	for _, c := range []struct{ name, value string }{{"model", m.Model}, {"effort", m.Effort}} {
		if c.value == "" {
			continue
		}
		if !agyValue.MatchString(c.value) {
			return invalid("not an Antigravity %s: %q", c.name, c.value)
		}
		if err := d.agyCommand(id, "/"+c.name+" "+c.value); err != nil {
			return err
		}
	}
	if m.Mode != "" {
		return d.paneMode(id, p, m.Mode, agyMode)
	}
	return nil
}

// agyCommand types command into agy's input box in pane, Enter with it,
// once agy waits there. agy's slash menu opens as the command is typed;
// an Enter in the same write takes the line as typed, while one sent
// after the menu drew would pick the menu's row.
func (d *Daemon) agyCommand(pane, command string) error {
	defer d.paneCommandLock(pane)()
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	p, busy, err := d.ready(pane)
	if err != nil {
		if busy {
			return fmt.Errorf("%w; try again when it waits at its input box", err)
		}
		return err
	}
	d.noteUserKey(pane, nil)
	return d.handle(context.Background(), proto.Input{Pane: pane, Data: append(input.Text(command), press(key.NameReturn, p.Modes())...)})
}

// agyResumed names the transcript of an agy pane resumed with
// --conversation: agy fires no hook until the next prompt, and its pane
// shows as a chat only once its transcript is known. The path is agy's own
// for the conversation the daemon resumed, under the HOME the pane runs
// with, and only a file that is there counts. Callers hold d.mu.
func (d *Daemon) agyResumed(p model.Pane) {
	if p.Provider != model.ProviderAntigravity || p.SessionID == "" {
		return
	}
	t := agent.AntigravityTranscript(homeOf(p.Env), p.SessionID)
	if t == "" {
		return
	}
	if _, err := os.Stat(t); err != nil {
		return
	}
	if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == p.ID }); i >= 0 {
		d.st.Panes[i].Transcript = t
	}
}

// agyRepair points an Antigravity pane whose saved SessionID is a subagent's
// back at its root main conversation when agy's on-disk parent records prove
// the link, updating or clearing Transcript to match.
func agyRepair(p *model.Pane) bool {
	if p.Provider != model.ProviderAntigravity || p.SessionID == "" {
		return false
	}
	home := homeOf(p.Env)
	main := agent.AgyMainConversation(home, p.SessionID)
	if main == "" || main == p.SessionID {
		return false
	}
	log.Printf("pane %s: saved Antigravity session %s is a subagent; restoring main conversation %s", p.ID, p.SessionID, main)
	p.SessionID = main
	if t := agent.AntigravityTranscript(home, main); t != "" {
		if _, err := os.Stat(t); err == nil {
			p.Transcript = t
			return true
		}
	}
	p.Transcript = ""
	return true
}

// agyForeignSubagent reports whether sid is a subagent conversation on disk,
// checking both the brain directory of transcript (when present) and home.
func agyForeignSubagent(home, transcript, sid string) bool {
	if sid == "" {
		return false
	}
	if b := agent.AgyBrainOfTranscript(transcript); b != "" && agent.AgyParent(b, sid) != "" {
		return true
	}
	if b := agent.AgyBrainDir(home); b != "" && agent.AgyParent(b, sid) != "" {
		return true
	}
	return false
}

// codexResumed names the transcript of a Codex pane resumed with `codex
// resume <id>`: Codex fires no hook until the next prompt either. The file
// is the rollout whose name carries the session id, under the CODEX_HOME
// the pane runs with. Callers hold d.mu.
func (d *Daemon) codexResumed(p model.Pane) {
	if p.Provider != model.ProviderCodex || p.SessionID == "" {
		return
	}
	dir := agent.CodexDir(homeOf(p.Env))
	for _, e := range p.Env {
		if h, ok := strings.CutPrefix(e, "CODEX_HOME="); ok && h != "" {
			dir = h
		}
	}
	t := agent.CodexRollout(dir, p.SessionID)
	if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == p.ID }); i >= 0 && t != "" {
		d.st.Panes[i].Transcript = t
	}
}

// agyTrustMsg is what agy's terminal report says while it asks whether to
// trust the folder it opened in.
const agyTrustMsg = "Trust this folder?"

// agyTrusting reports whether pane id's agy waits on its folder-trust
// question, as its latest report says. Callers hold d.mu.
func (d *Daemon) agyTrusting(id string) bool {
	pp := d.prog[id]
	if pp == nil {
		return false
	}
	for _, r := range pp.store.Records() {
		if r.ID == "" && r.EffectiveApp == agyApp {
			return r.State == progstatus.Blocked && r.Kind == progstatus.Permission && r.Msg == agyTrustMsg
		}
	}
	return false
}

// homeOf is the HOME a process started with env runs under: env's own,
// else the daemon's.
func homeOf(env []string) string {
	home, _ := os.UserHomeDir()
	for _, e := range env {
		if h, ok := strings.CutPrefix(e, "HOME="); ok {
			home = h
		}
	}
	return home
}

// agyTrustWorktree adds dir, a linked git worktree such as pitwall's
// worktrees under <repo>/.worktrees, to agy's trusted folders when agy
// already trusts its main repo, before agy starts there: agy asks again
// for every folder, a repo's worktrees included. A folder whose repo the
// user has not trusted in agy is left to agy's own question. It returns
// what it did, for the log, or "".
func agyTrustWorktree(dir string, env []string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	main := agent.WorktreeMain(dir)
	if main == "" {
		return ""
	}
	path := agent.AgySettings(homeOf(env))
	data, err := os.ReadFile(path)
	if err != nil {
		return "" // no settings: agy trusts nothing yet
	}
	out, ok, err := agent.AgyTrust(data, main, dir)
	if err != nil {
		return "agy's settings could not be read, so the worktree was not trusted: " + err.Error()
	}
	if !ok {
		return ""
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return "agy's settings could not be written: " + err.Error()
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(out)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return "agy's settings could not be written: " + err.Error()
	}
	return "added " + dir + " to agy's trusted folders, as agy trusts its repo " + main
}
