package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/quanticstudios/pitwall/internal/model"
)

// checkRemoteControl sets Pane.RemoteControl on each pane whose Claude Code
// session is reachable through /remote-control, and clears it everywhere
// while [appearance] remote_control is off.
func (d *Daemon) checkRemoteControl() {
	on := d.o.RemoteControl != nil && d.o.RemoteControl()
	d.mu.Lock()
	want := map[string]string{} // pane: the sessions dir to read
	sid := map[string]string{}
	for _, p := range d.st.Panes {
		if on && p.Provider == model.ProviderClaude && p.SessionID != "" && p.Transcript != "" {
			// <config>/projects/<project>/<session>.jsonl
			want[p.ID] = filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(p.Transcript))), "sessions")
			sid[p.ID] = p.SessionID
		}
	}
	d.mu.Unlock()

	live := map[string]map[string]bool{} // sessions dir: session id: remote control on
	for _, dir := range want {
		if live[dir] == nil {
			live[dir] = claudeBridges(dir)
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	changed := false
	for i := range d.st.Panes {
		p := &d.st.Panes[i]
		// why: the session may have changed while the files were read
		rc := want[p.ID] != "" && p.SessionID == sid[p.ID] && live[want[p.ID]][p.SessionID]
		if p.RemoteControl != rc {
			p.RemoteControl, changed = rc, true
		}
	}
	if changed {
		d.changed()
	}
}

// claudeBridges reads Claude Code's per-process files in dir
// (~/.claude/sessions/<pid>.json) and reports, per session id, whether its
// newest process has a Remote Control session: bridgeSessionId is set while
// /remote-control is on and null once it is off. A crashed process leaves
// its file behind, so an older file for the same session loses to a newer.
func claudeBridges(dir string) map[string]bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	on := map[string]bool{}
	newest := map[string]int64{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			SessionID string `json:"sessionId"`
			StartedAt int64  `json:"startedAt"`
			Bridge    string `json:"bridgeSessionId"`
		}
		if json.Unmarshal(b, &s) != nil || s.SessionID == "" {
			continue
		}
		if t, seen := newest[s.SessionID]; !seen || s.StartedAt > t {
			newest[s.SessionID], on[s.SessionID] = s.StartedAt, s.Bridge != ""
		}
	}
	return on
}
