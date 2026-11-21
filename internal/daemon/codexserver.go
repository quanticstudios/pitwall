package daemon

import (
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// codexRoute places a Codex hook that the hook process pid got from a
// Codex app-server. Codex 0.171 and later run every turn in one app-server
// per CODEX_HOME: the first Codex TUI starts it in a session of its own,
// later TUIs connect to it, and it outlives the TUI that started it. Its
// hooks, and those of any agent run from its tool shells, carry that first
// TUI's environment, so their PITWALL_PANE names the pane that started the
// server, not the pane whose turn it is. server reports the server's own
// hook; pane is the pane it belongs to, by its session_id and else its cwd
// (codexPane), or "" when no single pane can be told. nested reports a hook
// from an agent the server runs, such as `codex exec` from a tool shell.
func (d *Daemon) codexRoute(m proto.AgentEvent, pid int) (pane string, server, nested bool) {
	if pid <= 0 || m.Provider != model.ProviderCodex {
		return "", false, false
	}
	d.mu.Lock()
	if d.isChat(m.Pane) {
		d.mu.Unlock()
		return "", false, false // a chat runs an app-server of its own
	}
	live := make(map[string]Pane, len(d.panes))
	for id, p := range d.panes {
		live[id] = p
	}
	d.mu.Unlock()
	fgs := map[int]string{}
	for id, p := range live {
		if f, ok := p.(foregrounder); ok && f.Foreground() > 0 {
			fgs[f.Foreground()] = id
		}
	}
	switch codexServer(pid, fgs) {
	case serverHook:
		return d.codexPane(m.Payload, fgs, live), true, false
	case underServer:
		return "", false, true
	}
	return "", false, false
}

const (
	notServer   = iota
	serverHook  // the app-server ran the hook
	underServer // an agent the app-server runs ran it
)

// codexServer places the hook process pid against the Codex app-servers,
// given the panes' foreground groups: serverHook when only shells sit
// between the hook and an app-server, underServer when one is further up.
func codexServer(pid int, fgs map[int]string) int {
	c, _ := parentOf(pid)
	for range 64 { // a bound against a pid reused into a loop
		if c <= 1 || !isShell(commOf(c)) {
			break
		}
		c, _ = parentOf(c)
	}
	if isServer(c, fgs) {
		return serverHook
	}
	for range 64 {
		if c <= 1 {
			return notServer
		}
		if isServer(c, fgs) {
			return underServer
		}
		c, _ = parentOf(c)
	}
	return notServer
}

// isServer reports whether process c is a Codex app-server: a `codex` in no
// pane's foreground group (a pane's own Codex TUI is) that a Codex TUI
// leading a pane's foreground group started, or, once that TUI exited,
// that has no Codex and no pane's job above it.
func isServer(c int, fgs map[int]string) bool {
	if c <= 1 || commOf(c) != "codex" {
		return false
	}
	up, pg := parentOf(c)
	if pg <= 0 || fgs[pg] != "" {
		return false
	}
	if _, upg := parentOf(up); up > 1 && fgs[upg] != "" {
		return commOf(up) == "codex"
	}
	for range 64 {
		if up <= 1 {
			return true
		}
		next, pg := parentOf(up)
		if pg <= 0 || fgs[pg] != "" || commOf(up) == "codex" {
			return false
		}
		up = next
	}
	return false
}

func isShell(c string) bool { return c != "" && (c == shellComm || slices.Contains(shells, c)) }

// codexPane is the pane an app-server's Codex hook belongs to: the pane
// whose session it continues, else, for a session no pane holds yet, the
// one pane whose foreground Codex TUI runs in the hook's cwd. A hook
// without a transcript, as a /side fork's or an app-server's own short
// thread, only ever continues a session. Two Codex panes in one folder
// leave a new session unplaced: nothing the hook carries tells them apart.
func (d *Daemon) codexPane(payload []byte, fgs map[int]string, live map[string]Pane) string {
	var p struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	if p.SessionID != "" {
		d.mu.Lock()
		i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.SessionID == p.SessionID })
		id := ""
		if i >= 0 {
			id = d.st.Panes[i].ID
		}
		d.mu.Unlock()
		if id != "" {
			return id
		}
	}
	if agent.Transcript(payload) == "" || !filepath.IsAbs(p.Cwd) {
		return ""
	}
	found := ""
	for g, id := range fgs {
		if commOf(g) != "codex" || filepath.Clean(live[id].Cwd()) != filepath.Clean(p.Cwd) {
			continue
		}
		if found != "" {
			return ""
		}
		found = id
	}
	return found
}
