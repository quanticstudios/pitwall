package daemon

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
)

// terminalQuit bounds how long the terminal view's agent gets to quit on
// its keys before its pane is closed.
const terminalQuit = 3 * time.Second

// quitGap is the pause between the keys that quit the terminal's agent.
const quitGap = 150 * time.Millisecond

// chatTerminal flips Claude, Codex or Antigravity chat c between the chat
// and the agent's own TUI on the same conversation (proto.ChatTerminal).
// One process holds the session at a time: going to the terminal
// interrupts a running turn and closes the driver before the TUI resumes
// the session in a pane of the tab; coming back quits the TUI, adds the
// turns it ran to the chat, and resumes the driver. Runs on c's loop.
func (d *Daemon) chatTerminal(ctx context.Context, c *chat, on bool) error {
	if !on {
		d.leaveTerminal(c)
		return nil
	}
	if c.term != "" {
		return nil
	}
	cmd, err := terminalCmd(c)
	if err != nil {
		return err
	}
	if c.drv != nil && c.snap.Busy() {
		// A claude that never answers the interrupt is closed all the same.
		ictx, cancel := context.WithTimeout(ctx, chatClose)
		err := c.drv.Interrupt(ictx)
		cancel()
		if err != nil {
			log.Printf("chat %s: interrupt for the terminal: %q", c.id, err)
		}
		c.cfg.Held = true
	}
	c.stop("terminal")
	c.reconcile("Interrupted: continued in the terminal", "Expired: continued in the terminal before anyone answered")
	c.termLog, c.termOff = terminalLog(c.cfg), 0
	if fi, err := os.Stat(c.termLog); err == nil {
		c.termOff = fi.Size()
	}
	env := slices.DeleteFunc(slices.Clone(c.cfg.Env), func(e string) bool { return strings.HasPrefix(e, "PITWALL_PANE=") })
	d.mu.Lock()
	w := d.workspace(c.cfg.Workspace)
	if w == nil {
		d.mu.Unlock()
		return chatError{"not-found", "chat " + c.id + " has no tab"}
	}
	err = d.addTab(w, cmp.Or(c.cfg.Cwd, w.Path), cmd, env)
	if err == nil {
		c.term = layout.Panes(w.Tabs[len(w.Tabs)-1].Layout)[0]
		if i := slices.IndexFunc(d.st.Chats, func(x model.Chat) bool { return x.ID == c.id }); i >= 0 {
			d.st.Chats[i].Terminal = c.term // before d.mu goes, so exited finds it
		}
		d.changed()
	}
	d.mu.Unlock()
	if err != nil {
		return chatError{"provider", cmd[0] + " did not start in the terminal: " + err.Error()}
	}
	c.system("Continued in the terminal at " + time.Now().Format("15:04"))
	return nil
}

// terminalCmd is the argv that runs chat c's agent's TUI on its session,
// in the chat's model, effort and mode: left out, the TUI takes its own
// default, which can be a mode that asks less than the chat does.
func terminalCmd(c *chat) ([]string, error) {
	switch c.cfg.Provider {
	case model.ProviderClaude:
		if c.cfg.From != nil {
			// Claude names a fork's or rewind's session with its first
			// turn; until then Resume is the old one.
			return nil, chatError{"busy", "Send a message first: the rewound or forked conversation has no session yet"}
		}
		return claudeTerminal(c, cmp.Or(os.Getenv(claude.CommandEnv), "claude")), nil
	case model.ProviderCodex, model.ProviderAntigravity:
		if c.cfg.From != nil || c.cfg.Resume == "" || len(c.snap.Turns) == 0 {
			// why: Codex and agy write a conversation's file with its first turn, and their TUIs resume only a file.
			return nil, chatError{"busy", "Send a message first: the conversation has no turn for the terminal to resume"}
		}
		if c.cfg.Provider == model.ProviderCodex {
			return codexTerminal(c.cfg, cmp.Or(os.Getenv("PITWALL_CODEX_COMMAND"), "codex")), nil
		}
		bin := "agy"
		if p, ok := c.prov.(agyACP); ok {
			bin = p.Command
		}
		return agyTerminal(c.cfg, bin), nil
	}
	return nil, chatError{"unsupported", "only Claude, Codex and Antigravity chats have a terminal view"}
}

// claudeTerminal is claude on c's session: --resume once a turn wrote the
// session, else --session-id, which creates it under the same id, so the
// driver resumes it afterwards. It keeps the chat's permission mode,
// default included, as the TUI's own default can be auto mode.
func claudeTerminal(c *chat, bin string) []string {
	if c.cfg.Resume == "" {
		c.cfg.Resume = newUUID()
	}
	cmd := []string{bin, "--session-id", c.cfg.Resume}
	if len(c.snap.Turns) > 0 {
		cmd[1] = "--resume"
	}
	if c.cfg.Model != "" {
		cmd = append(cmd, "--model", c.cfg.Model)
	}
	return append(cmd, "--permission-mode", cmp.Or(c.cfg.Mode, "default"))
}

// codexTerminal is codex resume on cfg's thread, with the approval policy
// and sandbox the chat's driver gives its turns. --no-daemon keeps the
// turns in the TUI's own process: Codex's shared background server keeps
// the thread's writer lock after the TUI quits, and the driver's
// app-server could not resume it.
func codexTerminal(cfg session.Config, bin string) []string {
	approval, sandbox := "on-request", "workspace-write"
	switch cfg.Mode {
	case "bypassPermissions":
		approval = "never"
	case "plan":
		sandbox = "read-only"
	}
	cmd := []string{bin, "resume", cfg.Resume, "--no-daemon", "-a", approval, "-s", sandbox}
	if cfg.Model != "" {
		cmd = append(cmd, "-m", cfg.Model)
	}
	if cfg.Effort != "" {
		cmd = append(cmd, "-c", "model_reasoning_effort="+strconv.Quote(cfg.Effort))
	}
	return cmd
}

// agyTerminal is agy on cfg's conversation, which is the ACP session id.
// agy's own default mode (agentMode "") asks before edits and commands as
// the chat's default does; left out, agy 1.5.14 starts in auto.
func agyTerminal(cfg session.Config, bin string) []string {
	cmd := []string{bin, "--conversation", cfg.Resume}
	if cfg.Model != "" {
		cmd = append(cmd, "--model", cfg.Model)
	}
	if cfg.Effort != "" {
		cmd = append(cmd, "--effort", cfg.Effort)
	}
	if cfg.Mode == "" || cfg.Mode == "default" {
		return append(cmd, "--settings", `{"agentMode":""}`)
	}
	return append(cmd, "--mode", cfg.Mode)
}

// terminalLog is the transcript the TUI and the chat's driver both write
// for cfg's session, "" for Claude, whose chat does not read it.
func terminalLog(cfg session.Config) string {
	switch cfg.Provider {
	case model.ProviderCodex:
		dir := agent.CodexDir(homeOf(cfg.Env))
		for _, e := range cfg.Env {
			if h, ok := strings.CutPrefix(e, "CODEX_HOME="); ok && h != "" {
				dir = h
			}
		}
		return agent.CodexRollout(dir, cfg.Resume)
	case model.ProviderAntigravity:
		return agent.AntigravityTranscript(homeOf(cfg.Env), cfg.Resume)
	}
	return ""
}

// quitKeys are what the terminal's agent quits on, mid-turn too: Claude
// Code on two Ctrl+C, Codex and agy once Esc stopped a turn.
func quitKeys(p model.Provider) [][]byte {
	if p == model.ProviderClaude {
		return [][]byte{{3}, {3}}
	}
	return [][]byte{{0x1b}, {3}, {3}}
}

// leaveTerminal quits the terminal view's agent with its quit keys, closes
// its pane when it does not quit in time, and gives the session back to
// the driver. Runs on c's loop.
func (d *Daemon) leaveTerminal(c *chat) {
	id := c.term
	if id == "" {
		return
	}
	d.mu.Lock()
	p := d.panes[id]
	d.mu.Unlock()
	if p != nil {
		for i, k := range quitKeys(c.cfg.Provider) {
			if i > 0 {
				time.Sleep(quitGap)
			}
			p.Write(k)
		}
		select {
		case <-p.Done():
		case <-time.After(terminalQuit):
			log.Printf("chat %s: terminal %s did not quit in %v; closing its pane", c.id, c.cfg.Provider, terminalQuit)
		}
	}
	d.terminalGone(c, id)
}

// terminalGone takes chat c's terminal pane id out of its tab, if it is
// still there, and resumes the driver on the session. The pane exiting on
// its own or being closed calls it as well, so it does nothing once c has
// left that pane. Runs on c's loop.
func (d *Daemon) terminalGone(c *chat, id string) {
	if id == "" || c.term != id {
		return
	}
	c.term = ""
	d.mu.Lock()
	closing := d.removePane(id)
	if i := slices.IndexFunc(d.st.Chats, func(x model.Chat) bool { return x.ID == c.id }); i >= 0 {
		d.st.Chats[i].Terminal = ""
	}
	d.changed()
	d.mu.Unlock()
	closeAll(closing)
	d.terminalTurns(c)
	c.system("Back from the terminal")
	if err := d.chatStart(c); err != nil {
		log.Printf("chat %s: resume after the terminal: %q", c.id, err)
	}
}

// terminalTurns adds to chat c the turns its TUI ran: what the TUI wrote
// to the session's transcript after the driver closed, read as a pane
// chat reads it. Codex and agy give a resumed driver the history only as
// a whole, which the chat already shows up to the terminal. Runs on c's
// loop.
func (d *Daemon) terminalTurns(c *chat) {
	path, off := c.termLog, c.termOff
	c.termLog, c.termOff = "", 0
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("chat %s: the terminal's turns: %q", c.id, err)
		return
	}
	defer f.Close()
	var tr paneReader = codex.NewRollout()
	if c.cfg.Provider == model.ProviderAntigravity {
		tr = antigravity.NewTranscript()
	}
	for _, e := range readTurns(io.NewSectionReader(f, off, 1<<62), tr) {
		e.CreatedAt = cmp.Or(e.CreatedAt, session.Stamp(time.Now()))
		c.commit(session.Reduce(c.snap, e), e.CreatedAt)
	}
}

// readTurns is the events of the whole turns in r, a transcript read from
// where the TUI took over: what comes before the first turn, the end of
// the turn the chat stopped, is the chat's already. A turn still running
// at the end ends interrupted, its TUI gone.
func readTurns(r io.Reader, tr paneReader) []session.Event {
	var out []session.Event
	started := false
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		for _, e := range tr.Line(bytes.Clone(sc.Bytes())) {
			started = started || e.Type == session.EventTurnStarted
			if started {
				out = append(out, e)
			}
		}
	}
	if started && tr.Running() {
		out = append(out, tr.End(session.TurnInterrupted)...)
	}
	return out
}

// chatOfTerminal is the chat whose terminal view pane id is, nil for any
// other pane. Callers hold d.mu.
func (d *Daemon) chatOfTerminal(id string) *chat {
	if i := slices.IndexFunc(d.st.Chats, func(x model.Chat) bool { return x.Terminal == id }); id != "" && i >= 0 {
		return d.chats[d.st.Chats[i].ID]
	}
	return nil
}

// terminalClosed tells chat chatID that its tab lost its last pane, the
// terminal view's, so it shows the chat again. Callers hold d.mu.
func (d *Daemon) terminalClosed(chatID string, panes []string) {
	c := d.chats[chatID]
	if c == nil {
		return
	}
	for _, id := range panes {
		go c.do(func() { d.terminalGone(c, id); d.chatChanged(c) })
	}
}

// withoutChatTerminals leaves chat tabs' terminal views out of saved state:
// their claude stops with the daemon, and the chat resumes the session on
// its next command.
func withoutChatTerminals(s model.State) model.State {
	if !slices.ContainsFunc(s.Workspaces, func(w model.Workspace) bool { return w.Chat != "" && len(w.Tabs) > 0 }) {
		return s
	}
	s.Workspaces = slices.Clone(s.Workspaces)
	drop := map[string]bool{}
	for i, w := range s.Workspaces {
		if w.Chat != "" && len(w.Tabs) > 0 {
			drop[w.ID] = true
			s.Workspaces[i].Tabs, s.Workspaces[i].ActiveTab = nil, ""
		}
	}
	s.Panes = slices.DeleteFunc(slices.Clone(s.Panes), func(p model.Pane) bool { return drop[p.WorkspaceID] })
	return s
}
