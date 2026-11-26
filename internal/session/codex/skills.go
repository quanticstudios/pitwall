package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// skillsTimeout bounds a skills/list call, so a server that never answers
// one still lets the chat start.
const skillsTimeout = 10 * time.Second

// skills puts the skills app-server lists for the chat's folder in its
// / menu, read again from disk when reload. A server without skills/list
// leaves the menu as it was. The caller holds cmdMu.
func (d *driver) skills(ctx context.Context, p *proc, reload bool) {
	ctx, cancel := context.WithTimeout(ctx, skillsTimeout)
	defer cancel()
	params := map[string]any{}
	if d.cfg.Cwd != "" {
		params["cwds"] = []string{d.cfg.Cwd}
	}
	if reload {
		params["forceReload"] = true
	}
	res, err := d.call(ctx, p, "skills/list", params)
	if err != nil {
		return
	}
	d.post(session.Event{Type: session.EventCommands, Commands: listedSkills(res)})
}

// relist reads the skills again after skills/changed, unless the chat
// closed or its server changed since.
func (d *driver) relist() {
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	d.mu.Lock()
	p, closing := d.p, d.closing
	d.mu.Unlock()
	if !closing && p != nil {
		d.skills(context.Background(), p, true)
	}
}

// listedSkills is skills/list's result as commands, in its order, each
// named once; a skill the user turned off is left out.
func listedSkills(res json.RawMessage) []session.Command {
	var r struct {
		Data []struct {
			Skills []struct {
				Name, Description, ShortDescription, Scope, PluginID string
				Enabled                                              *bool
				Interface                                            struct{ ShortDescription string }
			}
		}
	}
	json.Unmarshal(res, &r)
	out := []session.Command{}
	seen := map[string]bool{}
	for _, e := range r.Data {
		for _, s := range e.Skills {
			if s.Name == "" || seen[s.Name] || s.Enabled != nil && !*s.Enabled {
				continue
			}
			seen[s.Name] = true
			out = append(out, session.Command{Name: s.Name, Description: cmp.Or(s.Interface.ShortDescription, s.ShortDescription, s.Description), Source: scopeSource(s.Scope, s.PluginID)})
		}
	}
	return out
}

// scopeSource is the menu's source for a skill's scope in Codex, and
// the plugin it comes with, "" for none.
func scopeSource(scope, plugin string) string {
	switch {
	case plugin != "":
		return "plugin"
	case scope == "repo":
		return "project"
	case scope == "system" || scope == "admin":
		return "system"
	}
	return "user"
}
