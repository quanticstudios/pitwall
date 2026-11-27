package mobile

import (
	"context"
	"errors"
	"image"
	"image/color"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// newForm is the New tab screen: on a machine of the session shown, a
// folder, an agent, a worktree or none, and a first message. Start opens
// the tab there and lands in its chat.
type newForm struct {
	on       bool
	session  string   // the session's name; "" lets the machine pick
	machines []string // the machines to pick from, by id
	machine  string
	opts     remote.NewOptions
	loaded   bool
	loadErr  string
	gen      int // counts loads and starts, so a late answer finds its form gone

	folder   int // in opts.Folders; len(opts.Folders) is a typed path
	agent    int // in opts.Agents
	worktree bool

	path, branch, brief          widget.Editor
	pathBox, branchBox, briefBox widget.Clickable
	// menu is the open dropdown: "machine", "folder", "agent" or
	// "worktree"; "" for none.
	menu                     string
	picks                    map[string]*widget.Clickable
	selects                  map[string]*widget.Clickable
	scrim, back, start, load widget.Clickable
	list, menuList           layout.List
	sending                  bool
	note                     string
	focus                    *widget.Editor // a pick asks to type here next frame
}

func (f *newForm) btn(m map[string]*widget.Clickable, k string) (map[string]*widget.Clickable, *widget.Clickable) {
	if m == nil {
		m = map[string]*widget.Clickable{}
	}
	if m[k] == nil {
		m[k] = new(widget.Clickable)
	}
	return m, m[k]
}

func (f *newForm) pick(k string) *widget.Clickable {
	var b *widget.Clickable
	f.picks, b = f.btn(f.picks, k)
	return b
}

func (f *newForm) sel(k string) *widget.Clickable {
	var b *widget.Clickable
	f.selects, b = f.btn(f.selects, k)
	return b
}

// setOptions takes what the machine offers: its first folder, else a
// typed path, and its first agent.
func (f *newForm) setOptions(o remote.NewOptions) {
	f.opts, f.loaded, f.loadErr = o, true, ""
	f.folder, f.agent, f.worktree = 0, 0, false
}

// typed reports whether the folder is a path typed in.
func (f *newForm) typed() bool { return f.folder >= len(f.opts.Folders) }

// canWorktree reports whether the folder can take a new worktree: a repo,
// or a typed path, which the machine checks.
func (f *newForm) canWorktree() bool { return f.typed() || f.opts.Folders[f.folder].Git }

// shell reports whether the agent picked is a plain terminal, which takes
// no first message.
func (f *newForm) shell() bool {
	return f.agent < len(f.opts.Agents) && f.opts.Agents[f.agent].ID == remote.Terminal
}

// request is the tab the form describes, or what is missing.
func (f *newForm) request() (remote.NewTab, error) {
	dir := ""
	if f.typed() {
		dir = strings.TrimSpace(f.path.Text())
	} else {
		dir = f.opts.Folders[f.folder].Path
	}
	switch {
	case dir == "":
		return remote.NewTab{}, errors.New("type the folder's path")
	case f.agent >= len(f.opts.Agents):
		return remote.NewTab{}, errors.New("this machine offers no agent")
	}
	r := remote.NewTab{Session: f.session, Dir: dir, Agent: f.opts.Agents[f.agent].ID}
	if f.worktree && f.canWorktree() {
		r.Worktree, r.Branch = true, strings.TrimSpace(f.branch.Text())
		if r.Branch == "" {
			return remote.NewTab{}, errors.New("name the new worktree's branch")
		}
	}
	if !f.shell() {
		r.Brief = strings.TrimSpace(f.brief.Text())
	}
	return r, nil
}

// newLinks are the machines the New form can start a tab on: those of
// the session shown that are online and serve the form, else, with no
// session, every such machine.
func (a *App) newLinks() []*link {
	ls := a.sessionLinks()
	if len(ls) == 0 && a.fleet != nil {
		for _, m := range a.fleet.Machines {
			ls = append(ls, a.links[m.ID])
		}
	}
	var out []*link
	for _, l := range ls {
		if l != nil && l.online && l.hello.Start {
			out = append(out, l)
		}
	}
	return out
}

// openNew opens the New form on the session shown.
func (a *App) openNew() {
	ls := a.newLinks()
	if len(ls) == 0 {
		return
	}
	f := &a.form
	*f = newForm{on: true}
	if s := a.st.Session(a.session); s != nil {
		f.session = s.Name
	}
	for _, l := range ls {
		f.machines = append(f.machines, l.m.ID)
	}
	f.path.SingleLine, f.branch.SingleLine = true, true
	f.list.Axis, f.menuList.Axis = layout.Vertical, layout.Vertical
	a.loadNew(ls[0].m.ID)
}

// loadNew asks machine what the form offers there.
func (a *App) loadNew(machine string) {
	f := &a.form
	f.machine, f.loaded, f.loadErr, f.note = machine, false, "", ""
	f.gen++
	gen, session := f.gen, f.session
	var o remote.NewOptions
	a.do(machine, func(ctx context.Context, c *phone.Client) (err error) {
		o, err = c.NewOptions(ctx, session)
		return err
	}, func(err error) {
		if !f.on || f.gen != gen {
			return
		}
		if err != nil {
			f.loadErr = sentence(err.Error())
			return
		}
		f.setOptions(o)
	})
}

// startNew sends the form's tab and, once it opens, shows its chat.
func (a *App) startNew() {
	f := &a.form
	r, err := f.request()
	if err != nil {
		f.note = sentence(err.Error())
		return
	}
	f.sending, f.note = true, ""
	f.gen++
	gen, machine := f.gen, f.machine
	var pane string
	a.do(machine, func(ctx context.Context, c *phone.Client) (err error) {
		_, pane, err = c.NewTab(ctx, r)
		return err
	}, func(err error) {
		if !f.on || f.gen != gen {
			return // the tab opened anyway; the list shows it
		}
		f.sending = false
		if err != nil {
			f.note = sentence(err.Error())
			return
		}
		a.form = newForm{}
		if r.Session != "" {
			a.session, a.picked = "s:"+r.Session, true
		}
		if l := a.links[machine]; l != nil && pane != "" {
			a.chat, a.chatM = newChat(pane), machine
			a.chatN++
			l.conn.Watch(pane)
		}
	})
}

// newLayout draws the New form, and its open dropdown over it.
func (a *App) newLayout(gtx C) {
	f, th := &a.form, a.th
	leave := f.back.Clicked(gtx)
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			if f.menu != "" {
				f.menu = ""
			} else {
				leave = true
			}
		}
	}
	if leave {
		gtx.Execute(key.FocusCmd{})
		a.form = newForm{}
		return
	}
	if f.load.Clicked(gtx) {
		a.loadNew(f.machine)
	}
	if f.start.Clicked(gtx) && f.loaded && !f.sending {
		a.startNew()
	}
	if f.focus != nil {
		gtx.Execute(key.FocusCmd{Tag: f.focus})
		gtx.Execute(key.SoftKeyboardCmd{Show: true})
		f.focus = nil
	}
	for _, k := range []string{"machine", "folder", "agent", "worktree"} {
		if f.sel(k).Clicked(gtx) {
			f.menu = k
		}
	}
	fillMax(gtx, th.Sidebar)
	ws := a.newFields()
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return a.bar(gtx, func(gtx C) D { return iconButton(gtx, th, &f.back, icon("chevron-left")) }, func(gtx C) D {
				return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Large), th.Fg, 1, "New tab")
			}, nil)
		}),
		layout.Flexed(1, func(gtx C) D {
			return layout.Inset{Left: pad, Right: pad, Top: 12, Bottom: pad}.Layout(gtx, func(gtx C) D {
				return f.list.Layout(gtx, len(ws), func(gtx C, i int) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return ws[i](gtx)
				})
			})
		}),
	)
	if f.menu != "" {
		a.newMenu(gtx)
	}
}

// newFields are the form's rows, top to bottom.
func (a *App) newFields() []layout.Widget {
	f, th := &a.form, a.th
	head := func(s string) layout.Widget {
		return func(gtx C) D {
			return layout.Inset{Bottom: 4}.Layout(gtx, func(gtx C) D { return small(gtx, th, th.Muted, s) })
		}
	}
	var ws []layout.Widget
	if len(f.machines) > 1 {
		ws = append(ws, head("Machine"), a.selectRow("machine", a.machineName(f.machine), "", ""), gap(12))
	}
	switch {
	case f.loadErr != "":
		return append(ws, func(gtx C) D { return small(gtx, th, th.Readable(th.Red, th.Sidebar), f.loadErr) }, gap(10),
			func(gtx C) D { return kit.WideButton(gtx, th, &f.load, kit.Secondary, kit.Large, "Try again") })
	case !f.loaded:
		return append(ws, func(gtx C) D { return small(gtx, th, th.Muted, "Loading…") })
	}
	folder, where := "Type a path", ""
	if !f.typed() {
		folder, where = f.opts.Folders[f.folder].Name, f.opts.Folders[f.folder].Path
	}
	ws = append(ws, head("Folder"), a.selectRow("folder", folder, where, f.opts.Home))
	if f.typed() {
		ws = append(ws, gap(6), func(gtx C) D {
			return field(gtx, th, &f.path, &f.pathBox, "~/project or /abs/path", th.MonoFont)
		})
	}
	agent := "None"
	if f.agent < len(f.opts.Agents) {
		agent = f.opts.Agents[f.agent].Name
	}
	ws = append(ws, gap(12), head("Agent"), a.selectRow("agent", agent, "", ""))
	if f.canWorktree() {
		wt := "None"
		if f.worktree {
			wt = "New worktree"
		}
		ws = append(ws, gap(12), head("Worktree"), a.selectRow("worktree", wt, "", ""))
		if f.worktree {
			ws = append(ws, gap(6), func(gtx C) D { return field(gtx, th, &f.branch, &f.branchBox, "Branch name", th.MonoFont) })
		}
	}
	if !f.shell() {
		ws = append(ws, gap(12), head("First message (optional)"), func(gtx C) D {
			gtx.Constraints.Min.Y = gtx.Dp(72)
			return field(gtx, th, &f.brief, &f.briefBox, "What should the agent do?", th.UIFont)
		})
	}
	if f.note != "" {
		ws = append(ws, gap(10), func(gtx C) D { return small(gtx, th, th.Readable(th.Red, th.Sidebar), f.note) })
	}
	text := "Start"
	if f.sending {
		text = "Starting…"
	}
	return append(ws, gap(16), func(gtx C) D { return kit.WideButton(gtx, th, &f.start, kit.Primary, kit.Large, text) })
}

func (a *App) machineName(id string) string {
	if l := a.links[id]; l != nil {
		return l.m.Name
	}
	return id
}

// selectRow is a dropdown's closed face: the value picked, a muted line
// under it when sub is set, and a chevron. A tap opens its menu. With home
// set, sub is a folder's path, cut in the middle to fit.
func (a *App) selectRow(k, value, sub, home string) layout.Widget {
	th, c := a.th, a.form.sel(k)
	return func(gtx C) D {
		return c.Layout(gtx, func(gtx C) D {
			fill := th.Bg
			if c.Pressed() || a.form.menu == k {
				fill = th.Pressed
			}
			return box(gtx, fill, th.BorderStrong, theme.RadiusControl, layout.Inset{Left: 10, Right: 8, Top: 8, Bottom: 8}, func(gtx C) D {
				gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(touch)-gtx.Dp(16))
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, 1, value) }),
							layout.Rigid(func(gtx C) D {
								if sub == "" {
									return D{}
								}
								if home != "" {
									return pathLabel(gtx, th, th.Muted, sub, home)
								}
								return label(gtx, th, th.MonoFont, th.Sp(theme.Caption), th.Muted, 1, sub)
							}),
						)
					}),
					layout.Rigid(func(gtx C) D { return sidebar.Icon(gtx, "chevron-down", gtx.Dp(14), th.Muted) }),
				)
			})
		})
	}
}

// pathLabel is a folder's path in caption mono, ~ for home, cut in the
// middle to the width so the end that tells folders apart shows.
func pathLabel(gtx C, th *theme.Theme, col color.NRGBA, path, home string) D {
	size := th.Sp(theme.Caption)
	// ponytail: a monospace advance is about 0.6 em, as screenSize takes it.
	n := int(float32(gtx.Constraints.Max.X) / (float32(gtx.Sp(size)) * 0.6))
	return label(gtx, th, th.MonoFont, size, col, 1, shortDir(path, home, n))
}

// shortDir is path with ~ for home, cut in the middle to n runes: its
// start and, at twice the length, its end.
func shortDir(path, home string, n int) string {
	if home != "" && home != "/" {
		if path == home {
			path = "~"
		} else if rest, ok := strings.CutPrefix(path, home); ok && (rest[0] == '/' || rest[0] == '\\') {
			path = "~" + rest
		}
	}
	r := []rune(path)
	if n < 8 || len(r) <= n {
		return path
	}
	head := (n - 1) / 3
	return string(r[:head]) + "…" + string(r[len(r)-(n-1-head):])
}

// choice is one row of a dropdown's menu.
type choice struct {
	key, name, sub string
	path           bool // sub is a folder's path
	on             bool
	pick           func()
}

// newChoices are the open dropdown's rows.
func (a *App) newChoices() (title string, cs []choice) {
	f := &a.form
	switch f.menu {
	case "machine":
		for _, id := range f.machines {
			cs = append(cs, choice{key: id, name: a.machineName(id), on: id == f.machine, pick: func() {
				if id != f.machine {
					a.loadNew(id)
				}
			}})
		}
		return "Machine", cs
	case "folder":
		for i, fo := range f.opts.Folders {
			cs = append(cs, choice{key: fo.Path, name: fo.Name, sub: fo.Path, path: true, on: i == f.folder, pick: func() { f.folder = i }})
		}
		n := len(f.opts.Folders)
		return "Folder", append(cs, choice{key: "\x00typed", name: "Type a path", on: f.typed(), pick: func() {
			f.folder = n
			f.focus = &f.path
		}})
	case "agent":
		for i, ag := range f.opts.Agents {
			cs = append(cs, choice{key: ag.ID, name: ag.Name, on: i == f.agent, pick: func() { f.agent = i }})
		}
		return "Agent", cs
	}
	return "Worktree", []choice{
		{key: "none", name: "None", sub: "Work in the folder itself", on: !f.worktree, pick: func() { f.worktree = false }},
		{key: "new", name: "New worktree", sub: "A new branch in .worktrees", on: f.worktree, pick: func() {
			f.worktree = true
			f.focus = &f.branch
		}},
	}
}

// newMenu draws the open dropdown's menu over a scrim, in the middle of
// the screen as Android's dialog spinners are; a tap on the scrim closes
// it.
func (a *App) newMenu(gtx C) {
	f, th := &a.form, a.th
	title, cs := a.newChoices()
	for _, c := range cs {
		if f.pick(f.menu + ":" + c.key).Clicked(gtx) {
			f.menu, f.note = "", ""
			c.pick()
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	if f.scrim.Clicked(gtx) {
		f.menu = ""
		return
	}
	full := gtx.Constraints.Max
	f.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, th.Scrim, clip.Rect{Max: full}.Op())
		return D{Size: full}
	})
	w := min(full.X-2*gtx.Dp(pad), gtx.Dp(300))
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, full.Y*3/4)}
	m := op.Record(gtx.Ops)
	d := layout.UniformInset(4).Layout(g, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(menuHeading(th, title)),
			layout.Rigid(func(gtx C) D {
				return f.menuList.Layout(gtx, len(cs), func(gtx C, i int) D {
					c := cs[i]
					return menuRow(gtx, th, f.pick(f.menu+":"+c.key), c.on, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, 1, c.name) }),
									layout.Rigid(func(gtx C) D {
										if c.sub == "" {
											return D{}
										}
										if c.path {
											return pathLabel(gtx, th, th.Muted, c.sub, f.opts.Home)
										}
										return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, 1, c.sub)
									}),
								)
							}),
							layout.Rigid(func(gtx C) D {
								if !c.on {
									return D{}
								}
								return sidebar.Icon(gtx, "check", gtx.Dp(14), th.Primary)
							}),
						)
					})
				})
			}),
		)
	})
	call := m.Stop()
	at := image.Pt((full.X-d.Size.X)/2, max(gtx.Dp(barH), (full.Y-d.Size.Y)/3))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: d.Size}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	defer clip.Rect(rect).Push(gtx.Ops).Pop()
	// why: a tap on the panel between rows must not reach the scrim under it.
	(&widget.Clickable{}).Layout(gtx, func(gtx C) D { return D{Size: d.Size} })
	call.Add(gtx.Ops)
}
