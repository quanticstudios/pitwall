package daemon

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// newSession opens a tab holding m.Cmd or a shell in m.Cwd ("" means $HOME), named
// m.Name or unnamed, in m.SessionID (see proto.NewSession). Without a
// group, it joins the session's project whose folder holds its directory;
// ungrouped, it goes last.
func (d *Daemon) newSession(ctx context.Context, m proto.NewSession) error {
	return d.addSession(ctx, m, "", nil, nil)
}

// addSession is newSession, placing the tab right after the tab after, in
// its group or, when after is ungrouped, at the top level. With create set
// the tab goes into a new session with create's name and origin. prep,
// when set, fills in the tab before its pane starts, under d.mu.
func (d *Daemon) addSession(ctx context.Context, m proto.NewSession, after string, create *model.Session, prep func(*model.Workspace)) error {
	unlock, err := d.lockTabStart(ctx, m.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if m.Monitor {
		m.Cwd, m.Cmd, m.Env, m.Loose = "", nil, nil, m.GroupID == ""
	}
	if m.Profile != "" {
		s := config.Settings{}
		if d.o.Profiles != nil {
			s.Profiles = d.o.Profiles()
		}
		var err error
		m.Env, err = s.ProfileEnv(m.Profile, m.Env)
		if err != nil {
			return err
		}
	}
	home := homeDir()
	path := m.Cwd
	if m.FromPane != "" && !m.Monitor {
		d.mu.Lock()
		p := d.panes[m.FromPane]
		d.mu.Unlock()
		if p != nil {
			if c := p.Cwd(); c != "" {
				path = c
			}
		}
	}
	if path == "" {
		path = home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok || path == "~" {
		path = filepath.Join(home, rest) // pitwall new --host box '~/src', home on box
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(path); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	branch, _ := d.o.Branch(ctx, path)
	root := d.repoRoot(ctx, path)

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.closing || d.handedOff {
		return errors.New("daemon is closing")
	}
	if m.ID != "" && m.ParentPane == "" {
		// A retry that names its session and parent, as remote starts do,
		// matches before resolving them: its session may have closed with it.
		// A local parent must resolve first, even when the raw ParentTab
		// happens to match an existing root tab.
		if started, _ := d.startedTab(m.ID, m.SessionID, m.ParentTab); started {
			return nil
		}
	}
	at, sessions := len(d.st.Workspaces), len(d.st.Sessions)
	switch {
	case create != nil:
		name := create.Name
		if create.Origin != (model.Origin{}) {
			// The lookup, free name and creation share the same lock: other
			// clients may have made a part while we read the folder above.
			if d.st.PartOf(create.Origin) != nil {
				return nil
			}
			for n, base := 2, name; name != "" && d.st.SessionNamed(name) != nil; n++ {
				name = base + "-" + strconv.Itoa(n)
			}
		} else if name != "" && d.st.SessionNamed(name) != nil {
			return fmt.Errorf("a session is already named %s", name)
		}
		m.SessionID, m.GroupID = d.makeSession(name), ""
		d.st.Session(m.SessionID).Origin = create.Origin
	case after != "":
		i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == after })
		if i < 0 {
			return fmt.Errorf("no workspace %s", after)
		}
		at, m.GroupID, m.SessionID = i+1, d.st.Workspaces[i].ProjectID, d.st.Workspaces[i].SessionID
	default:
		if m.GroupID != "" {
			m.SessionID = d.st.SessionOf(m.GroupID)
		}
		if m.SessionID, err = d.targetSession(m.SessionID, m.FromPane); err != nil {
			return err
		}
		if parent := d.workspace(d.parentTab(m.ParentPane, m.SessionID)); parent != nil && m.GroupID == "" {
			m.GroupID = parent.ProjectID // hang under the parent, in its group or none
		} else if m.GroupID == "" && !m.Loose {
			m.GroupID = d.projectAt(m.SessionID, path)
		}
	}
	parent := d.parentTab(m.ParentPane, m.SessionID)
	if m.ParentTab != "" && m.ParentPane == "" {
		parent = m.ParentTab // a tab on another machine
	}
	if m.ID != "" {
		// A retry that leaves its session or parent for the daemon to find
		// compares against what they resolve to.
		if started, err := d.startedTab(m.ID, m.SessionID, parent); started || err != nil {
			d.st.Sessions = d.st.Sessions[:sessions] // drop one made for this tab
			return err
		}
	}
	if m.Monitor && d.showMonitor(m.SessionID) {
		return nil
	}
	if m.GroupID != "" && d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	if m.Name != "" && d.nameTaken(m.SessionID, m.Name, "") {
		return fmt.Errorf("a tab is already named %s", m.Name)
	}
	w := model.Workspace{ID: cmp.Or(m.ID, newID()), SessionID: m.SessionID, ProjectID: m.GroupID, Name: m.Name, NameSet: m.Name != "", Branch: branch, Path: path, RepoRoot: root, UpdatedAt: time.Now()}
	w.ClientID = m.ID != ""
	w.Parent, w.RemoteParent = parent, m.ParentTab != "" && m.ParentPane == ""
	w.Profile = m.Profile
	w.Monitor = m.Monitor
	if prep != nil {
		prep(&w)
	}
	if w.Chat != "" || w.Monitor {
		// A chat tab shows its chat, and a Monitor tab its machine, not panes.
	} else if err := d.addTab(&w, path, m.Cmd, m.Env); err != nil {
		d.st.Sessions = d.st.Sessions[:sessions] // drop one made for this tab
		return err
	}
	d.st.Workspaces = slices.Insert(d.st.Workspaces, at, w)
	d.rememberStart(w, m.ParentPane)
	if after != "" && m.GroupID == "" {
		d.st.PlaceTopAfter(w.ID, after)
	}
	d.changed()
	if branch != "" {
		go d.refreshStats(ctx, w.ID)
	}
	return nil
}

// lockTabStart serializes requests for one client-chosen id, including
// their worktree creation outside d.mu. A disconnected caller can retry
// while that work is still running without making another worktree.
func (d *Daemon) lockTabStart(ctx context.Context, id string) (func(), error) {
	if id == "" {
		return func() {}, nil
	}
	for {
		d.mu.Lock()
		if d.tabStarts == nil {
			d.tabStarts = make(map[string]chan struct{})
		}
		if pending := d.tabStarts[id]; pending != nil {
			d.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		pending := make(chan struct{})
		d.tabStarts[id] = pending
		d.mu.Unlock()
		return func() {
			d.mu.Lock()
			delete(d.tabStarts, id)
			close(pending)
			d.mu.Unlock()
		}, nil
	}
}

// startedTab reports whether tab id, asked for in session with parent
// (proto.NewSession.ID), is there already, live or closed, from the same
// request. An id that is not 16 hex digits, or another tab's, is an error.
// Callers hold d.mu.
func (d *Daemon) startedTab(id, session, parent string) (bool, error) {
	if _, err := hex.DecodeString(id); err != nil || len(id) != 16 {
		return false, fmt.Errorf("tab id %q is not 16 hex digits", id)
	}
	same := func(w model.Workspace) bool { return w.SessionID == session && w.Parent == parent }
	taken := fmt.Errorf("tab id %s belongs to another tab", id)
	if w := d.workspace(id); w != nil {
		if same(*w) {
			return true, nil
		}
		return false, taken
	}
	for _, c := range d.st.Closed {
		if c.Tab.ID == id && same(c.Tab) {
			return true, nil
		}
		if c.ID == id || c.Tab.ID == id {
			return false, taken
		}
	}
	if start, ok := d.st.Starts[id]; ok {
		if start.SessionID == session && start.Parent == parent {
			return true, nil
		}
		return false, taken
	}
	return false, nil
}

// maxStartIDs bounds the start history. A full history evicts an id whose
// tab is neither open nor in the reopen archive: one that closed at least
// that archive's 200 closes ago, far past any retry.
const maxStartIDs = 65_536

// rememberStart retains a successfully created tab's original identity.
// Callers hold d.mu.
func (d *Daemon) rememberStart(w model.Workspace, parentPane string) {
	if !w.ClientID {
		return
	}
	if d.st.Starts == nil {
		d.st.Starts = map[string]model.TabStart{}
	}
	if _, ok := d.st.Starts[w.ID]; !ok && len(d.st.Starts) >= maxStartIDs {
		for id := range d.st.Starts {
			if d.workspace(id) == nil && !slices.ContainsFunc(d.st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == id }) {
				delete(d.st.Starts, id)
				break
			}
		}
	}
	start := model.TabStart{SessionID: w.SessionID, Parent: w.Parent, ParentPane: parentPane}
	if s := d.st.Session(w.SessionID); s != nil {
		start.SessionName = s.Name
	}
	d.st.Starts[w.ID] = start
}

// showMonitor shows session's Monitor tab in the windows on that session,
// and reports whether it has one. The caller holds d.mu.
func (d *Daemon) showMonitor(session string) bool {
	i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.Monitor && w.SessionID == session })
	if i < 0 {
		return false
	}
	w := &d.st.Workspaces[i]
	w.Detached = false
	d.changed()
	d.forward(session, proto.FocusSession{WorkspaceID: w.ID, SessionID: session})
	return true
}

// parentTab is the tab of pane when it is in session, else "". The caller
// holds d.mu.
func (d *Daemon) parentTab(pane, session string) string {
	if w := d.paneTab(pane); w != nil && w.SessionID == session {
		return w.ID
	}
	return ""
}

// paneTab is the tab holding pane, or the tab of chat pane, or nil. The
// caller holds d.mu.
func (d *Daemon) paneTab(pane string) *model.Workspace {
	if pane == "" {
		return nil
	}
	ws, _, _, ok := d.agentOf(pane)
	if !ok {
		return nil
	}
	return d.workspace(ws)
}

func (d *Daemon) newGroup(m proto.NewGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	session := ""
	for i, id := range m.WorkspaceIDs {
		w := d.workspace(id)
		if w == nil {
			return fmt.Errorf("no workspace %s", id)
		}
		if i > 0 && w.SessionID != session {
			return errors.New("a group's tabs must be in one session")
		}
		session = w.SessionID
	}
	if session == "" {
		if s := d.st.Recent(); s != nil {
			session = s.ID
		}
	}
	name := m.Name
	for n := 1; name == ""; n++ {
		if s := fmt.Sprintf("Group %d", n); !slices.ContainsFunc(d.st.Projects, func(p model.Project) bool { return p.SessionID == session && p.Name == s }) {
			name = s
		}
	}
	// ponytail: the UI's color list is unexported in internal/ui/theme, so
	// groups start "neutral"; the user picks a color with SetProjectAppearance.
	g := model.Project{ID: newID(), SessionID: session, Name: name, Kind: model.ProjectGroup, Color: "neutral"}
	d.st.Projects = append(d.st.Projects, g)
	d.st.PlaceTop(g.ID, d.firstTop(d.topSlots(m.WorkspaceIDs)))
	for _, id := range m.WorkspaceIDs {
		d.workspace(id).ProjectID = g.ID
	}
	d.changed()
	return nil
}

// setSessionGroup moves a session into a group, last, or ungroups it right
// after the group it was in.
func (d *Daemon) setSessionGroup(m proto.SetSessionGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.GroupID != "" && d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	if m.GroupID != "" && d.project(m.GroupID).SessionID != w.SessionID {
		return fmt.Errorf("group %s is in another session", m.GroupID)
	}
	old := w.ProjectID
	w.ProjectID = m.GroupID
	if m.GroupID == "" && old != "" {
		d.st.PlaceTopAfter(w.ID, old)
	}
	d.changed()
	return nil
}

// topSlots maps each session to the top-level item it shows under: itself
// when ungrouped, else its group. Callers hold d.mu.
func (d *Daemon) topSlots(ids []string) []string {
	var out []string
	for _, id := range ids {
		if w := d.workspace(id); w != nil && w.ProjectID != "" && d.project(w.ProjectID) != nil {
			out = append(out, w.ProjectID)
		} else {
			out = append(out, id)
		}
	}
	return out
}

func (d *Daemon) renameGroup(m proto.RenameGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.project(m.GroupID)
	if p == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("group name is empty")
	}
	p.Name = m.Name
	d.changed()
	return nil
}

// deleteGroup ungroups the group's sessions into the group's place, in
// their order, and drops the group; their panes and directories stay.
func (d *Daemon) deleteGroup(m proto.DeleteGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	d.st.DeleteGroup(m.GroupID)
	d.changed()
	return nil
}

// project returns a pointer into d.st; callers hold d.mu.
func (d *Daemon) project(id string) *model.Project {
	if i := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == id }); i >= 0 {
		return &d.st.Projects[i]
	}
	return nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// gitBranch reports whether path is in a git work tree and its branch there,
// "" for a detached HEAD.
func gitBranch(ctx context.Context, path string) (string, bool) {
	out, err := gitstat.Command(ctx, path, "symbolic-ref", "--short", "-q", "HEAD").Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(string(out)), true
	case errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil:
		return "", true // detached
	}
	return "", false
}
