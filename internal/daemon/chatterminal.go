package daemon

import (
	"cmp"
	"context"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// terminalQuit bounds how long the terminal view's claude gets to quit on
// Ctrl+C before its pane is closed.
const terminalQuit = 3 * time.Second

// quitGap is the pause between the two Ctrl+C that quit Claude Code.
const quitGap = 150 * time.Millisecond

// chatTerminal flips Claude chat c between the chat and Claude Code's own
// TUI on the same conversation (proto.ChatTerminal). One process holds the
// session at a time: going to the terminal interrupts a running turn and
// closes the driver before claude --resume starts in a pane of the tab;
// coming back quits that claude before the driver resumes. Runs on c's
// loop.
func (d *Daemon) chatTerminal(ctx context.Context, c *chat, on bool) error {
	if !on {
		d.leaveTerminal(c)
		return nil
	}
	if c.term != "" {
		return nil
	}
	if c.cfg.Provider != model.ProviderClaude {
		return chatError{"unsupported", "only a Claude chat has a terminal view"}
	}
	if c.cfg.From != nil {
		// Claude names a fork's or rewind's session with its first turn;
		// until then Resume is the old one.
		return chatError{"busy", "Send a message first: the rewound or forked conversation has no session yet"}
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
	cmd := terminalCmd(c, cmp.Or(os.Getenv(claude.CommandEnv), "claude"))
	env := slices.DeleteFunc(slices.Clone(c.cfg.Env), func(e string) bool { return strings.HasPrefix(e, "PITWALL_PANE=") })
	d.mu.Lock()
	w := d.workspace(c.cfg.Workspace)
	if w == nil {
		d.mu.Unlock()
		return chatError{"not-found", "chat " + c.id + " has no tab"}
	}
	err := d.addTab(w, cmp.Or(c.cfg.Cwd, w.Path), cmd, env)
	if err == nil {
		c.term = layout.Panes(w.Tabs[len(w.Tabs)-1].Layout)[0]
		if i := slices.IndexFunc(d.st.Chats, func(x model.Chat) bool { return x.ID == c.id }); i >= 0 {
			d.st.Chats[i].Terminal = c.term // before d.mu goes, so exited finds it
		}
		d.changed()
	}
	d.mu.Unlock()
	if err != nil {
		return chatError{"provider", "claude did not start in the terminal: " + err.Error()}
	}
	c.system("Continued in the terminal at " + time.Now().Format("15:04"))
	return nil
}

// terminalCmd is the argv that runs Claude Code's TUI on c's session:
// --resume once a turn wrote the session, else --session-id, which
// creates it under the same id, so the driver resumes it afterwards. It
// keeps the chat's model and permission mode, default included: left
// out, the TUI takes its own default, which can be auto mode.
func terminalCmd(c *chat, bin string) []string {
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

// leaveTerminal quits the terminal view's claude with two Ctrl+C, closes
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
		p.Write([]byte{3})
		time.Sleep(quitGap)
		p.Write([]byte{3})
		select {
		case <-p.Done():
		case <-time.After(terminalQuit):
			log.Printf("chat %s: terminal claude did not quit in %v; closing its pane", c.id, terminalQuit)
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
	c.system("Back from the terminal")
	if err := d.chatStart(c); err != nil {
		log.Printf("chat %s: resume after the terminal: %q", c.id, err)
	}
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
