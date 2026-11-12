package mobile

import (
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// machinesView is the gear's screen: the paired machines, each renamed in
// place or removed, and Add machine.
type machinesView struct {
	back, add widget.Clickable
	names     map[string]*widget.Editor    // by machine id
	remove    map[string]*widget.Clickable // by machine id
	armed     string                       // the machine whose Remove asks for a second tap
	note      string
	list      layout.List
}

func (v *machinesView) editor(id, name string) *widget.Editor {
	if v.names == nil {
		v.names, v.remove = map[string]*widget.Editor{}, map[string]*widget.Clickable{}
	}
	ed := v.names[id]
	if ed == nil {
		ed = &widget.Editor{SingleLine: true, Submit: true}
		ed.SetText(name)
		v.names[id] = ed
		v.remove[id] = new(widget.Clickable)
	}
	return ed
}

func (a *App) machinesLayout(gtx C) {
	v, th := &a.mach, a.th
	v.list.Axis = layout.Vertical
	leave := v.back.Clicked(gtx)
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			leave = true
		}
	}
	if v.add.Clicked(gtx) {
		a.adding = true
		return
	}
	for _, m := range a.fleet.Machines {
		ed := v.editor(m.ID, m.Name)
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				gtx.Execute(key.FocusCmd{})
			}
		}
		// A name takes effect once the field lets go of the focus.
		if name := strings.TrimSpace(ed.Text()); !gtx.Focused(ed) && name != m.Name {
			if name == "" {
				ed.SetText(m.Name)
			} else {
				a.renameMachine(m.ID, name)
				ed.SetText(a.links[m.ID].m.Name)
			}
		}
		if v.remove[m.ID].Clicked(gtx) {
			if v.armed != m.ID {
				v.armed = m.ID
			} else {
				a.removeMachine(m.ID, "")
				a.mach = machinesView{}
				return
			}
		}
	}
	if leave {
		gtx.Execute(key.FocusCmd{})
		a.machines, a.mach = false, machinesView{}
		return
	}

	fillMax(gtx, th.Sidebar)
	var ws []layout.Widget
	for _, m := range a.fleet.Machines {
		id := m.ID
		ws = append(ws, func(gtx C) D { return a.machineCard(gtx, id) }, gap(10))
	}
	if v.note != "" {
		ws = append(ws, func(gtx C) D { return small(gtx, th, th.Readable(th.Red, th.Sidebar), v.note) }, gap(10))
	}
	ws = append(ws, func(gtx C) D { return kit.WideButton(gtx, th, &v.add, kit.Secondary, kit.Large, "Add machine") },
		gap(16), a.updateToggle, gap(12), a.updateSettings,
		gap(16), func(gtx C) D {
			return layout.Center.Layout(gtx, func(gtx C) D {
				return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, 1, "pitwall for Android "+Version)
			})
		})
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return a.bar(gtx, func(gtx C) D { return iconButton(gtx, th, &v.back, icon("chevron-left")) }, func(gtx C) D {
				return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Large), th.Fg, 1, "Settings")
			}, nil)
		}),
		layout.Flexed(1, func(gtx C) D {
			return layout.Inset{Left: pad, Right: pad, Top: 12, Bottom: pad}.Layout(gtx, func(gtx C) D {
				return v.list.Layout(gtx, len(ws), func(gtx C, i int) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return ws[i](gtx)
				})
			})
		}),
	)
}

// machineCard is one paired machine: its name to edit, its address, how
// its connection is going, and Remove.
func (a *App) machineCard(gtx C, id string) D {
	v, th := &a.mach, a.th
	l := a.links[id]
	status := "Connecting…"
	switch {
	case l.online:
		status = "Connected"
		if l.hello.Device != "" {
			status += " as " + l.hello.Device
		}
	case l.offline != "":
		status = offlineText(l.retryAt, gtx.Now)
	}
	removeText := "Remove"
	if v.armed == id {
		removeText = "Tap again to remove"
	}
	return box(gtx, th.Surface, th.BorderSubtle, theme.RadiusControl+2, layout.Inset{Left: 10, Right: 4, Top: 6, Bottom: 6}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D {
							return sidebar.Icon(gtx, "server", gtx.Dp(14), linkColor(th, l))
						})
					}),
					layout.Flexed(1, func(gtx C) D {
						return pairField(gtx, th, v.names[id], "Name", th.UIFont, false, nil)
					}),
					layout.Rigid(func(gtx C) D {
						if l.hello.Version == "" {
							return D{}
						}
						return layout.Inset{Left: 8, Right: 6}.Layout(gtx, func(gtx C) D {
							return label(gtx, th, th.MonoFont, th.Sp(theme.Caption), th.Muted, 1, l.hello.Version)
						})
					}),
				)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 6, Left: 22}.Layout(gtx, func(gtx C) D {
					return label(gtx, th, th.MonoFont, th.Sp(theme.Caption), th.Muted, 1, l.m.Server)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Inset{Left: 22}.Layout(gtx, func(gtx C) D {
							return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, 1, status)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return redButton(gtx, th, v.remove[id], removeText)
					}),
				)
			}),
		)
	})
}

// redButton is textButton in the danger color.
func redButton(gtx C, th *theme.Theme, c *widget.Clickable, s string) D {
	t := *th
	t.Primary = th.Readable(th.Red, th.Surface)
	return textButton(gtx, &t, c, s)
}
