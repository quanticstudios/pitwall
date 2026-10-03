package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// freshName is an adjective-noun name no session has. Callers hold d.mu.
func (d *Daemon) freshName() string {
	for n := 0; ; n++ {
		if name := model.SessionName(rand.IntN, n); d.st.SessionNamed(name) == nil {
			return name
		}
	}
}

// sessionName checks a name a person chose for session id ("" for a new
// one). Callers hold d.mu.
func (d *Daemon) sessionName(name, id string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("session name is empty")
	}
	if s := d.st.SessionNamed(name); s != nil && s.ID != id {
		return "", fmt.Errorf("a session is already named %s", name)
	}
	return name, nil
}

// targetSession is the session a new tab goes to: id when set, else the
// pane's session, else the most recently used one. With no session at all
// it makes one. Callers hold d.mu.
func (d *Daemon) targetSession(id, pane string) (string, error) {
	if id != "" {
		if d.st.Session(id) == nil {
			return "", fmt.Errorf("no session %s", id)
		}
		return id, nil
	}
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane }); pane != "" && i >= 0 {
		if w := d.workspace(d.st.Panes[i].WorkspaceID); w != nil {
			return w.SessionID, nil
		}
	}
	if s := d.st.Recent(); s != nil {
		return s.ID, nil
	}
	return d.makeSession(""), nil
}

// makeSession adds an empty session named name (generated when "") and
// returns its id. Callers hold d.mu.
func (d *Daemon) makeSession(name string) string {
	if name == "" {
		name = d.freshName()
	}
	s := model.Session{ID: newID(), Name: name, UsedAt: time.Now()}
	d.st.Sessions = append(d.st.Sessions, s)
	return s.ID
}

// sessionNew makes a session with a shell tab.
func (d *Daemon) sessionNew(ctx context.Context, m proto.SessionNew) error {
	d.mu.Lock()
	name := ""
	if m.Name != "" {
		var err error
		if name, err = d.sessionName(m.Name, ""); err != nil {
			d.mu.Unlock()
			return err
		}
	}
	d.mu.Unlock()
	return d.addSession(ctx, proto.NewSession{Cwd: m.Cwd, FromPane: m.FromPane}, "", &name)
}

func (d *Daemon) sessionRename(m proto.SessionRename) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.st.Session(m.SessionID)
	if s == nil {
		return fmt.Errorf("no session %s", m.SessionID)
	}
	name, err := d.sessionName(m.Name, s.ID)
	if err != nil {
		return err
	}
	s.Name = name
	d.changed()
	return nil
}

// sessionKill closes every pane of the session and forgets its tabs and
// groups; the session ends with them.
func (d *Daemon) sessionKill(m proto.SessionKill) error {
	d.mu.Lock()
	if d.st.Session(m.SessionID) == nil {
		d.mu.Unlock()
		return fmt.Errorf("no session %s", m.SessionID)
	}
	var closing []Pane
	for _, w := range slices.Clone(d.st.Workspaces) {
		if w.SessionID == m.SessionID {
			closing = append(closing, d.removeWorkspace(w.ID)...)
		}
	}
	d.endSession(m.SessionID) // one with only empty groups had no tab to end it
	d.changed()
	d.mu.Unlock()
	closeAll(closing)
	return nil
}

// sessionShow records that client c shows session id.
func (d *Daemon) sessionShow(c *client, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.st.Session(id)
	if s == nil {
		return fmt.Errorf("no session %s", id)
	}
	c.session, s.UsedAt = id, time.Now()
	d.changed()
	return nil
}

// endSession drops session, with its groups, once it has no tabs left, the
// way a tmux session ends with its last window. Callers hold d.mu.
func (d *Daemon) endSession(session string) {
	if slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.SessionID == session }) {
		return
	}
	d.st.Sessions = slices.DeleteFunc(d.st.Sessions, func(s model.Session) bool { return s.ID == session })
	d.st.Projects = slices.DeleteFunc(d.st.Projects, func(p model.Project) bool { return p.SessionID == session })
}

// adopt puts every tab and group whose session is unknown into the first
// session, made as "main" when there is none. Callers hold d.mu.
func (d *Daemon) adopt() {
	home := ""
	for _, s := range d.st.Sessions {
		home = s.ID
		break
	}
	orphan := func(id string) bool { return d.st.Session(id) == nil }
	for i := range d.st.Workspaces {
		if w := &d.st.Workspaces[i]; orphan(w.SessionID) {
			if home == "" {
				home = d.makeSession("main")
			}
			w.SessionID = home
		}
	}
	for i := range d.st.Projects {
		if p := &d.st.Projects[i]; orphan(p.SessionID) {
			if home == "" {
				home = d.makeSession("main")
			}
			p.SessionID = home
		}
	}
}

// fixOrders repairs every session's Order. Callers hold d.mu.
func (d *Daemon) fixOrders() {
	for i := range d.st.Sessions {
		d.st.Sessions[i].Order = d.st.TopOrder(d.st.Sessions[i].ID)
	}
}

// firstSession readies the session a GUI opens on: the one named name,
// made with a shell in cwd when missing, or with name "" the most recently
// used one, made when there is none. A session whose tabs are all detached
// gets a shell, so a window always lands in one. helloMu keeps two GUIs
// that connect at once from making two.
// windowsOn counts the GUI windows showing session id. Callers hold d.mu.
// ponytail: a window counts once it sends SessionShow, so two pitwalls
// started in the same instant can both take the same free session.
func (d *Daemon) windowsOn(id string) int {
	n := 0
	for c := range d.clients {
		if c.session == id {
			n++
		}
	}
	return n
}

func (d *Daemon) firstSession(ctx context.Context, cwd, name string) error {
	d.helloMu.Lock()
	defer d.helloMu.Unlock()
	if fi, err := os.Stat(cwd); cwd == "" || err != nil || !fi.IsDir() {
		cwd = ""
	}
	d.mu.Lock()
	// A window without -s takes the most recent session no window shows, so
	// a second pitwall opens a new session in its folder instead of a copy.
	var s *model.Session
	if name != "" {
		s = d.st.SessionNamed(name)
	} else {
		for i := range d.st.Sessions {
			c := &d.st.Sessions[i]
			if d.windowsOn(c.ID) == 0 && (s == nil || c.UsedAt.After(s.UsedAt)) {
				s = c
			}
		}
	}
	if s == nil {
		d.mu.Unlock()
		return d.addSession(ctx, proto.NewSession{Cwd: cwd}, "", &name)
	}
	id := s.ID
	open := slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.SessionID == id && !w.Detached })
	d.mu.Unlock()
	if open {
		return nil
	}
	return d.newSession(ctx, proto.NewSession{Cwd: cwd, SessionID: id})
}
