package app

import (
	"path/filepath"

	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// runCode is a code block's Run in chat id: it types cmd into the shell
// pane split beside the chat and gives that pane the keys, or, with none,
// puts cmd in the chat's composer. Either way the user presses Enter to
// run it; nothing here sends one.
func (u *ui) runCode(st *model.State, id, cmd string) {
	if sh := shellBeside(st, id); sh != "" {
		_, m, _ := u.b.Frame(sh)
		if !m.BracketedPaste {
			cmd = panel.OneLine(cmd) // a bare newline would run the first line
		}
		if u.send(proto.Input{Pane: sh, Data: input.Paste(cmd, m)}) {
			u.nav.setFocus(sh)
		}
		return
	}
	c := u.composer(st, id)
	if t := c.Text(); t != "" {
		cmd = t + "\n" + cmd // keep the draft
	}
	c.SetText(cmd)
}

// shellBeside is the first shell pane in the tab that shows pane chat id,
// "" for a chat tab or a tab without one. A shell pane is one running the
// user's shell, not an agent, a command or a program that exited.
func shellBeside(st *model.State, id string) string {
	p := findPane(st, id)
	if p == nil {
		return ""
	}
	ws := findWorkspace(st, p.WorkspaceID)
	if ws == nil {
		return ""
	}
	for _, t := range ws.Tabs {
		if t.ID != ws.ActiveTab {
			continue
		}
		for _, pid := range layout.Panes(t.Layout) {
			if q := findPane(st, pid); pid != id && q != nil && isShell(q) {
				return pid
			}
		}
	}
	return ""
}

// isShell reports whether p runs a shell at rest: no agent, not exited,
// started as the user's shell or a shell by name.
func isShell(p *model.Pane) bool {
	if p.Provider != "" || p.Exited || p.Held {
		return false
	}
	if len(p.Cmd) == 0 {
		return true
	}
	switch filepath.Base(p.Cmd[0]) {
	case "sh", "bash", "zsh", "fish", "ksh", "dash", "nu":
		return len(p.Cmd) == 1 || len(p.Cmd) == 2 && (p.Cmd[1] == "-l" || p.Cmd[1] == "-i")
	}
	return false
}
