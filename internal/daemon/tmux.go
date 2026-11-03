package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/tmuxshim"
)

var tmuxSpecs = map[string]tmuxshim.Spec{
	"split-window":      {Bools: "dhvPbf", Values: "tcFelp"},
	"respawn-pane":      {Bools: "k", Values: "tce"},
	"send-keys":         {Bools: "lH", Values: "tN"},
	"list-panes":        {Bools: "as", Values: "tF"},
	"display-message":   {Bools: "p", Values: "tF"},
	"kill-pane":         {Values: "t"},
	"select-pane":       {Values: "tTP"},
	"capture-pane":      {Bools: "p", Values: "tSE"},
	"has-session":       {Values: "t"},
	"list-windows":      {Bools: "a", Values: "tF"},
	"list-sessions":     {Values: "F"},
	"rename-window":     {Values: "t"},
	"set-option":        {Bools: "gpwqu", Values: "t"},
	"set-window-option": {Bools: "gqu", Values: "t"},
	"select-layout":     {Values: "t"},
	"resize-pane":       {Values: "txy"},
}

var tmuxAliases = map[string]string{
	"splitw": "split-window", "respawnp": "respawn-pane", "send": "send-keys",
	"lsp": "list-panes", "display": "display-message", "killp": "kill-pane",
	"selectp": "select-pane", "capturep": "capture-pane", "has": "has-session",
	"lsw": "list-windows", "ls": "list-sessions", "renamew": "rename-window",
	"set": "set-option", "setw": "set-window-option", "selectl": "select-layout", "resizep": "resize-pane",
}

func (d *Daemon) tmux(m proto.Tmux) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("the tmux shim requires Unix PTYs")
	}
	if len(m.Args) == 0 {
		return "", errors.New("tmux shim: a command is required")
	}
	name := m.Args[0]
	if alias := tmuxAliases[name]; alias != "" {
		name = alias
	}
	sp, ok := tmuxSpecs[name]
	if !ok {
		return "", fmt.Errorf("tmux shim: unknown command %q", name)
	}
	p, err := tmuxshim.ParseFlags(name, sp, m.Args[1:])
	if err != nil {
		return "", err
	}
	if name != "split-window" && name != "respawn-pane" && name != "send-keys" && len(p.Args) > 2 {
		return "", errors.New("tmux shim: too many arguments")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	origin := d.tmuxPane(m.From)
	if origin == nil {
		return "", errors.New("tmux-shim needs a running pitwall pane")
	}
	w := d.workspace(origin.WorkspaceID)
	ti := paneTab(w, origin.ID)
	if ti < 0 {
		return "", errors.New("tmux shim: caller has no tab")
	}
	tab := &w.Tabs[ti]
	target, _ := p.Value('t')
	pane, err := d.tmuxTarget(w, tab, origin.ID, target)
	if err != nil {
		return "", err
	}
	vars := d.tmuxVars(w, tab, pane, origin.ID)
	format := func(f string) (string, error) { return tmuxshim.Expand(f, vars) }
	printFormat := func(f string) (string, error) { s, e := format(f); return s + "\n", e }
	switch name {
	case "display-message":
		if len(p.Args) > 1 {
			return "", errors.New("display-message: expected one format")
		}
		f, has := p.Value('F')
		if len(p.Args) == 1 {
			if has {
				return "", errors.New("display-message: format given twice")
			}
			f = p.Args[0]
		}
		if !p.Has('p') {
			return "", nil
		}
		if f == "" {
			f = "#{session_name}:#{window_index}.#{pane_index}"
		}
		return printFormat(f)
	case "list-panes":
		if len(p.Args) != 0 {
			return "", errors.New("list-panes: unexpected argument")
		}
		f, ok := p.Value('F')
		if !ok {
			f = "#{pane_index}: [#{pane_width}x#{pane_height}] #{pane_id}#{?pane_active, (active),}"
		}
		var out strings.Builder
		for _, id := range layout.Panes(tab.Layout) {
			v := d.tmuxVars(w, tab, d.tmuxPane(id), origin.ID)
			line, e := tmuxshim.Expand(f, v)
			if e != nil {
				return "", e
			}
			out.WriteString(line + "\n")
		}
		return out.String(), nil
	case "list-windows", "list-sessions":
		if len(p.Args) != 0 {
			return "", errors.New("tmux list: unexpected argument")
		}
		f, ok := p.Value('F')
		if !ok {
			if name == "list-windows" {
				f = "#{window_index}: #{window_name} (#{window_panes} panes)"
			} else {
				f = "#{session_name}: 1 windows"
			}
		}
		return printFormat(f)
	case "has-session":
		if len(p.Args) != 0 {
			return "", errors.New("has-session: unexpected argument")
		}
		return "", nil
	case "split-window":
		if p.Has('h') && p.Has('v') {
			return "", errors.New("split-window: choose -h or -v")
		}
		if p.Has('b') || p.Has('f') {
			return "", errors.New("split-window: -b and -f are unsupported")
		}
		size := 0.5
		for _, flag := range []byte{'l', 'p'} {
			if value, ok := p.Value(flag); ok {
				n, e := tmuxSize(value, flag == 'p')
				if e != nil {
					return "", e
				}
				size = n
			}
		}
		// Validate the format before creating a pane.
		f, ok := p.Value('F')
		if !ok {
			f = "#{session_name}:#{window_index}.#{pane_index}"
		}
		if _, e := format(f); e != nil {
			return "", e
		}
		cwd := pane.Cwd
		if live := d.panes[pane.ID]; live != nil && live.Cwd() != "" {
			cwd = live.Cwd()
		}
		if dir, ok := p.Value('c'); ok {
			cwd = dir
		}
		id := newID()
		env, e := tmuxEnv(d.profileEnv(w), m.Env, p.Values('e'), id)
		if e != nil {
			return "", e
		}
		cmd := tmuxCommand(p.Args)
		if e = d.start(id, cmd, cwd, w.Ports, env); e != nil {
			return "", e
		}
		d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: cmd, Cwd: cwd, Env: env})
		dir := layout.Vertical
		if p.Has('h') {
			dir = layout.Horizontal
		}
		tab.Layout = d.o.Split(tab.Layout, pane.ID, id, dir)
		tmuxSplitRatio(tab.Layout, pane.ID, id, size)
		d.changed()
		if !p.Has('d') {
			d.tmuxFocus(w, tab, id)
		}
		if p.Has('P') {
			vars = d.tmuxVars(w, tab, d.tmuxPane(id), origin.ID)
			return printFormat(f)
		}
		return "", nil
	case "respawn-pane":
		if !slices.Contains(pane.Env, "TMUX_PANE="+tmuxshim.ID("%", pane.ID)) {
			return "", errors.New("respawn-pane: target was not opened by the tmux shim")
		}
		if !p.Has('k') && !pane.Exited {
			return "", errors.New("respawn-pane: pane is running; use -k")
		}
		cmd := tmuxCommand(p.Args)
		if len(p.Args) == 0 {
			cmd = pane.Cmd
		}
		cwd := pane.Cwd
		if dir, ok := p.Value('c'); ok {
			cwd = dir
		}
		env, e := tmuxEnv(pane.Env, m.Env, p.Values('e'), pane.ID)
		if e != nil {
			return "", e
		}
		id := pane.ID
		size := d.sizes[id]
		if d.panes[id] == nil {
			return "", errors.New("respawn-pane: pane is already being replaced")
		}
		held, holdFailed := pane.Held && !pane.TmuxHoldFailed, pane.TmuxHoldFailed
		old := d.dropPane(id)
		d.mu.Unlock()
		e = old.Close()
		d.mu.Lock()
		pane = d.tmuxPane(id)
		if pane == nil || d.closing {
			return "", errors.New("respawn-pane: pane closed during replacement")
		}
		if e == nil {
			d.sizes[id] = size
			e = d.start(id, cmd, cwd, w.Ports, env)
		}
		if e != nil {
			closeAll(d.removePane(id))
			d.changed()
			return "", e
		}
		title := pane.TmuxTitle
		*pane = model.Pane{ID: id, WorkspaceID: pane.WorkspaceID, Cmd: cmd, Cwd: cwd, Env: env, Title: title, TmuxTitle: title, Held: held, TmuxHoldFailed: holdFailed}
		d.changed()
		next := d.panes[id]
		d.mu.Unlock()
		d.pushFrame(id, next)
		d.mu.Lock()
		return "", nil
	case "send-keys":
		if p.Has('l') && p.Has('H') {
			return "", errors.New("send-keys: -l and -H conflict")
		}
		text, e := tmuxshim.SendKeysText(p.Args, p.Has('l'), p.Has('H'))
		if e != nil {
			return "", e
		}
		n := 1
		if value, ok := p.Value('N'); ok {
			n, e = strconv.Atoi(value)
			if e != nil || n < 1 || n > 10000 {
				return "", errors.New("send-keys: -N must be between 1 and 10000")
			}
		}
		if len(text) > 1<<20 || n > (1<<20)/max(1, len(text)) {
			return "", errors.New("send-keys: input exceeds 1 MiB")
		}
		id := pane.ID
		d.mu.Unlock()
		e = d.handle(context.Background(), proto.Input{Pane: id, Data: []byte(strings.Repeat(text, n))})
		d.mu.Lock()
		return "", e
	case "kill-pane":
		if len(p.Args) != 0 {
			return "", errors.New("kill-pane: unexpected argument")
		}
		if w.Pinned && d.st.PaneCount(w.ID) == 1 {
			return "", errors.New("kill-pane: tab is pinned")
		}
		closeAll(d.removePane(pane.ID))
		d.changed()
		return "", nil
	case "select-pane":
		if len(p.Args) != 0 {
			return "", errors.New("select-pane: unexpected argument")
		}
		if title, ok := p.Value('T'); ok {
			pane.TmuxTitle, pane.Title = title, title
			d.changed()
			return "", nil
		}
		if _, ok := p.Value('P'); !ok {
			d.tmuxFocus(w, tab, pane.ID)
		}
		return "", nil
	case "rename-window":
		if len(p.Args) != 1 {
			return "", errors.New("rename-window: expected one name")
		}
		if d.nameTaken(w.SessionID, p.Args[0], w.ID) {
			return "", errors.New("rename-window: name already taken")
		}
		w.Name, w.NameSet = p.Args[0], true
		d.changed()
		return "", nil
	case "capture-pane":
		if !p.Has('p') || len(p.Args) != 0 {
			return "", errors.New("capture-pane: only -p output is supported")
		}
		live := d.panes[pane.ID]
		if live == nil {
			return "", errors.New("capture-pane: pane has no terminal")
		}
		rows := live.Snapshot().Rows
		line := func(flag byte, def int) (int, error) {
			s, ok := p.Value(flag)
			if !ok {
				return def, nil
			}
			if s == "-" {
				if flag == 'S' {
					return -live.ScrollbackLen(), nil
				}
				return rows - 1, nil
			}
			n, e := strconv.Atoi(s)
			if e != nil {
				return 0, fmt.Errorf("capture-pane: invalid -%c", flag)
			}
			return n, nil
		}
		start, e := line('S', 0)
		if e != nil {
			return "", e
		}
		end, e := line('E', rows-1)
		if e != nil {
			return "", e
		}
		start = max(start, -live.ScrollbackLen())
		end = min(end, rows-1)
		if start > end {
			return "", nil
		}
		var out strings.Builder
		for n := start; n <= end; n++ {
			grid := live.SnapshotAt(max(0, -n))
			row := max(0, n)
			var b strings.Builder
			for x := 0; x < grid.Cols; x++ {
				idx := row*grid.Cols + x
				if idx < len(grid.Cells) {
					cell := grid.Cells[idx]
					if cell.Width > 0 {
						if cell.Content == "" {
							b.WriteByte(' ')
						} else {
							b.WriteString(cell.Content)
						}
					}
				}
			}
			out.WriteString(strings.TrimRight(b.String(), " ") + "\n")
		}
		return out.String(), nil
	case "set-option", "set-window-option":
		if len(p.Args) < 1 || len(p.Args) > 2 {
			return "", errors.New("set-option: expected option and value")
		}
		switch p.Args[0] {
		case "window-style", "pane-border-style", "pane-active-border-style", "pane-border-format":
			return "", nil
		case "remain-on-exit":
			if len(p.Args) != 2 || !slices.Contains([]string{"off", "on", "failed"}, p.Args[1]) {
				return "", errors.New("set-option: remain-on-exit needs off, on or failed")
			}
			pane.Held = p.Args[1] == "on"
			pane.TmuxHoldFailed = p.Args[1] == "failed"
			d.changed()
			return "", nil
		default:
			return "", fmt.Errorf("tmux shim: unsupported option %q", p.Args[0])
		}
	case "select-layout":
		if len(p.Args) != 1 || !slices.Contains([]string{"main-vertical", "main-horizontal", "even-horizontal", "even-vertical", "tiled"}, p.Args[0]) {
			return "", errors.New("select-layout: unsupported layout")
		}
		return "", nil
	case "resize-pane":
		if len(p.Args) != 0 {
			return "", errors.New("resize-pane: unexpected argument")
		}
		for _, flag := range []byte{'x', 'y'} {
			if value, ok := p.Value(flag); ok {
				if _, e := tmuxSize(value, false); e != nil {
					return "", e
				}
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("tmux shim: unknown command %q", name)
}

func (d *Daemon) tmuxPane(id string) *model.Pane {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 {
		return nil
	}
	return &d.st.Panes[i]
}

// tmuxTarget limits even explicit session/window targets to the caller's tab.
func (d *Daemon) tmuxTarget(w *model.Workspace, t *model.Tab, current, target string) (*model.Pane, error) {
	if target == "" {
		return d.tmuxPane(current), nil
	}
	session := "pitwall-" + w.ID
	if s := d.st.Session(w.SessionID); s != nil {
		session = s.Name
	}
	if a, b, ok := strings.Cut(target, ":"); ok {
		if a != "" && a != session && a != tmuxshim.ID("$", w.SessionID) {
			return nil, fmt.Errorf("tmux shim: can't find session %q", a)
		}
		target = b
	}
	window, pane, hasPane := strings.Cut(target, ".")
	if target == session || target == tmuxshim.ID("$", w.SessionID) {
		return d.tmuxPane(current), nil
	}
	if window == "" || window == "0" || window == tmuxshim.ID("@", w.ID) || window == w.Name || window == w.Label {
		if !hasPane {
			return d.tmuxPane(current), nil
		}
		n, err := strconv.Atoi(pane)
		ids := layout.Panes(t.Layout)
		if err == nil && n >= 0 && n < len(ids) {
			return d.tmuxPane(ids[n]), nil
		}
	}
	for _, id := range layout.Panes(t.Layout) {
		if target == tmuxshim.ID("%", id) || target == id || target == "%"+id {
			return d.tmuxPane(id), nil
		}
	}
	return nil, fmt.Errorf("tmux shim: can't find pane or window %q in caller's tab", target)
}

func (d *Daemon) tmuxVars(w *model.Workspace, t *model.Tab, p *model.Pane, current string) map[string]string {
	ids := layout.Panes(t.Layout)
	index := slices.Index(ids, p.ID)
	cols, rows := defaultCols, defaultRows
	if s, ok := d.sizes[p.ID]; ok {
		cols, rows = s[0], s[1]
	}
	focused := current
	for c := range d.clients {
		if slices.Contains(ids, c.focus) {
			focused = c.focus
			break
		}
	}
	active := "0"
	if p.ID == focused {
		active = "1"
	}
	sessionName := "pitwall-" + w.ID
	if s := d.st.Session(w.SessionID); s != nil {
		sessionName = s.Name
	}
	command := filepath.Base(pane.Shell())
	if len(p.Cmd) > 0 {
		command = filepath.Base(p.Cmd[0])
	}
	if live, ok := d.panes[p.ID].(foregrounder); ok {
		if name := commOf(live.Foreground()); name != "" {
			command = name
		}
	}
	host, _ := os.Hostname()
	short, _, _ := strings.Cut(host, ".")
	dead := "0"
	if p.Exited {
		dead = "1"
	}
	history := ""
	if live := d.panes[p.ID]; live != nil {
		history = strconv.Itoa(live.ScrollbackLen())
	}
	return map[string]string{
		"pane_id": tmuxshim.ID("%", p.ID), "pane_index": strconv.Itoa(index), "pane_title": p.Title,
		"pane_current_path": p.Cwd, "pane_current_command": command, "pane_active": active, "pane_dead": dead,
		"pane_width": strconv.Itoa(cols), "pane_height": strconv.Itoa(rows), "pane_in_mode": "0", "history_size": history,
		"window_id": tmuxshim.ID("@", w.ID), "window_index": "0", "window_name": w.Label, "window_active": "1", "window_panes": strconv.Itoa(len(ids)), "window_flags": "",
		"session_id": tmuxshim.ID("$", w.SessionID), "session_name": sessionName, "session_windows": "1", "host": host, "host_short": short,
		"version": "3.4", "pid": strconv.Itoa(os.Getpid()),
	}
}

func (d *Daemon) tmuxFocus(w *model.Workspace, t *model.Tab, id string) {
	d.forward(w.SessionID, proto.FocusSession{WorkspaceID: w.ID, TabID: t.ID, SessionID: w.SessionID, Pane: id})
}

func tmuxCommand(args []string) []string {
	if len(args) == 1 {
		return []string{"/bin/sh", "-c", args[0]}
	}
	return args
}

func tmuxEnv(base, shim, extra []string, id string) ([]string, error) {
	env := append([]string(nil), base...)
	for _, kv := range shim {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "PITWALL_SOCKET=") {
			return nil, errors.New("tmux shim: invalid launcher environment")
		}
		env = append(env, kv)
	}
	for _, kv := range extra {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.ContainsAny(k, "\x00\n") || strings.ContainsRune(kv, 0) {
			return nil, errors.New("tmux shim: -e needs NAME=value")
		}
		env = append(env, kv)
	}
	env = append(env, shim...)
	return append(env, "TMUX_PANE="+tmuxshim.ID("%", id)), nil
}

func tmuxSize(s string, percent bool) (float64, error) {
	value := strings.TrimSuffix(s, "%")
	if value != s {
		percent = true
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || percent && n >= 100 {
		return 0, errors.New("tmux shim: size must be positive, or a percentage below 100")
	}
	if percent {
		return float64(n) / 100, nil
	}
	// shortcut: cell sizes use an equal split, until the GUI reports the containing rectangle.
	return .5, nil
}

func tmuxSplitRatio(n *layout.Node, target, id string, size float64) {
	if len(n.Children) == 2 && n.Children[0].Pane == target && n.Children[1].Pane == id {
		n.Ratios = []float64{1 - size, size}
		return
	}
	for _, child := range n.Children {
		tmuxSplitRatio(child, target, id, size)
	}
}
