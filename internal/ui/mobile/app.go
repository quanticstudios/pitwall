// Package mobile is pitwall's phone app: the sidebar's tree of the paired
// machines' tabs, each agent tab's chat, and pairing, over the phone
// page's API (internal/phone). It runs on Android and as a desktop
// window.
package mobile

import (
	"context"
	"errors"
	"slices"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/op"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// zoom scales the desktop's sizes for a finger: its 32dp controls come out
// at 44dp. ponytail: one scale for every phone; tune it here.
const zoom = 1.375

// Options sets the window's title and colors, for app.NewWindow.
func Options(th *theme.Theme) []app.Option {
	return []app.Option{app.Title("pitwall"), app.StatusColor(th.Sidebar), app.NavigationColor(th.Bg)}
}

// Theme is aide's dark theme at the app's text size.
func Theme() *theme.Theme {
	t, _ := config.Builtin("aide-dark")
	th, _ := theme.New(t, config.Font{UISize: 12}) // 16.5sp once zoomed
	return th
}

// App is the app's state; Run owns it, on the window's goroutine. Other
// goroutines hand it work through post.
type App struct {
	w   *app.Window
	th  *theme.Theme
	dir string
	ui  chan func()

	fleet   *phone.Fleet // nil while no machine is paired
	links   map[string]*link
	focused bool

	tabs    []remote.Tab
	st      model.State
	session string // the State session the list shows
	picked  bool   // the user picked it; else the first session shows
	sb      sidebar.Sidebar

	// adding shows pairing over the sidebar, for another machine;
	// machines shows the list of paired machines.
	adding, machines bool

	pair  pairView
	list  listView
	mach  machinesView
	chat  chatView
	chatM string // the machine the open chat's tab is on
}

// link is one paired machine's stream and how it is going.
type link struct {
	m       phone.Machine
	conn    *phone.Conn
	online  bool
	offline string    // why the stream dropped; "" while online
	retryAt time.Time // when the stream tries again while offline
	since   time.Time // when it went offline
	hello   phone.Hello
}

// Run runs the app in w, with its machines kept in dir, until w closes.
func Run(w *app.Window, th *theme.Theme, dir string) error {
	a := &App{w: w, th: th, dir: dir, ui: make(chan func(), 256)}
	a.sb.TreeOnly, a.sb.Fill, a.sb.Touch, a.sb.ExpandAll, a.sb.Answers = true, true, true, true, true
	a.pair.init()
	if ms, err := phone.LoadMachines(dir); err != nil {
		a.pair.note = sentence(err.Error())
	} else if len(ms) > 0 {
		a.setMachines(ms)
	}
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			a.stop()
			return e.Err
		case app.ConfigEvent:
			// why: back in the foreground, a stream from before the phone slept may be dead without an error.
			if e.Config.Focused && !a.focused {
				for _, l := range a.links {
					l.conn.Retry()
				}
			}
			a.focused = e.Config.Focused
		case app.FrameEvent:
			a.drain()
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, th.Bg)
			gtx.Metric.PxPerDp *= zoom
			gtx.Metric.PxPerSp *= zoom
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// post runs f on the window's goroutine before its next frame.
func (a *App) post(f func()) {
	a.ui <- f
	a.w.Invalidate()
}

func (a *App) drain() {
	for {
		select {
		case f := <-a.ui:
			f()
		default:
			return
		}
	}
}

// do runs call with machine's client off the window's goroutine and then
// done on it, with call's error; ErrUnpaired removes the machine.
func (a *App) do(machine string, call func(ctx context.Context, c *phone.Client) error, done func(error)) {
	if a.fleet == nil || a.fleet.Client(machine) == nil {
		if done != nil {
			done(errors.New("that machine is no longer paired"))
		}
		return
	}
	f, c := a.fleet, a.fleet.Client(machine)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := call(ctx, c)
		a.post(func() {
			if errors.Is(err, phone.ErrUnpaired) && a.fleet == f {
				a.removeMachine(machine, err.Error())
				return
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

func (a *App) stop() {
	for _, l := range a.links {
		l.conn.Stop()
	}
}

// setMachines makes ms the paired machines and opens a stream to each.
// The lists the machines sent stay until their streams send new ones.
func (a *App) setMachines(ms []phone.Machine) {
	a.stop()
	old := a.fleet
	a.fleet, a.links = nil, map[string]*link{}
	if len(ms) == 0 {
		a.tabs, a.st, a.session = nil, model.State{}, ""
		a.chat, a.chatM, a.machines = chatView{}, "", false
		return
	}
	f := phone.NewFleet(ms)
	for _, m := range ms {
		if old != nil {
			f.Set(m.ID, old.TabsOf(m.ID))
		}
	}
	a.fleet = f
	a.refresh()
	for _, m := range ms {
		l := &link{m: m}
		a.links[m.ID] = l
		l.conn = &phone.Conn{Client: f.Client(m.ID),
			On: func(e phone.Event) { a.post(func() { a.event(f, l, e) }) },
			Status: func(on bool, err error, retry time.Time) {
				a.post(func() {
					if a.fleet != f {
						return
					}
					if !on && l.online || l.since.IsZero() {
						l.since = time.Now()
					}
					l.online, l.offline, l.retryAt = on, "", retry
					if errors.Is(err, phone.ErrUnpaired) {
						a.removeMachine(m.ID, m.Name+": "+err.Error())
					} else if err != nil {
						l.offline = err.Error()
					}
				})
			}}
		pane := ""
		if a.chatM == m.ID {
			pane = a.chat.pane
		}
		l.conn.Start(pane)
	}
}

// addMachine saves c's machine with the others and connects to it.
func (a *App) addMachine(c phone.Creds) error {
	var ms []phone.Machine
	if a.fleet != nil {
		ms = a.fleet.Machines
	}
	ms, _ = phone.AddMachine(ms, c)
	if err := phone.SaveMachines(a.dir, ms); err != nil {
		return err
	}
	a.adding = false
	a.setMachines(ms)
	return nil
}

// removeMachine forgets machine id, saying why on the pairing screen
// when it was the last one.
func (a *App) removeMachine(id, why string) {
	if a.fleet == nil {
		return
	}
	ms := slices.DeleteFunc(slices.Clone(a.fleet.Machines), func(m phone.Machine) bool { return m.ID == id })
	if err := phone.SaveMachines(a.dir, ms); err != nil {
		why = err.Error()
	}
	if a.chatM == id {
		a.chat, a.chatM = chatView{}, ""
	}
	a.setMachines(ms)
	if len(ms) == 0 {
		a.pair.note = sentence(why)
	} else if why != "" {
		a.list.note = sentence(why)
	}
}

// renameMachine names machine id name, made unique.
func (a *App) renameMachine(id, name string) {
	ms := slices.Clone(a.fleet.Machines)
	i := slices.IndexFunc(ms, func(m phone.Machine) bool { return m.ID == id })
	if i < 0 || name == "" {
		return
	}
	ms[i].Name = phone.UniqueName(ms, name, id)
	if err := phone.SaveMachines(a.dir, ms); err != nil {
		a.mach.note = sentence(err.Error())
		return
	}
	a.fleet.Machines = ms
	a.links[id].m = ms[i]
}

// refresh takes the fleet's merged list and keeps the session shown.
func (a *App) refresh() {
	a.tabs, a.st = a.fleet.Tabs(), a.fleet.State()
	if !a.picked || a.st.Session(a.session) == nil {
		a.session, a.picked = "", false
		if len(a.st.Sessions) > 0 {
			a.session = a.st.Sessions[0].ID
		}
	}
	if t, ok := a.tab(); ok {
		a.chat.data.Update(t, time.Now())
	}
}

func (a *App) event(f *phone.Fleet, l *link, e phone.Event) {
	if a.fleet != f {
		return
	}
	switch e.Name {
	case "hello":
		l.hello = e.Hello
	case "tabs":
		f.Set(l.m.ID, e.Tabs)
		a.refresh()
	case "reset":
		if e.Pane == a.chat.pane && a.chat.pane != "" && a.chatM == l.m.ID {
			a.chat.reset(e.Entries)
		}
	case "entries":
		if e.Pane == a.chat.pane && a.chat.pane != "" && a.chatM == l.m.ID {
			a.chat.add(e.Entries)
		}
	}
}

// tab is the open chat's tab in the latest list.
func (a *App) tab() (remote.Tab, bool) {
	if a.chat.pane == "" {
		return remote.Tab{}, false
	}
	i := slices.IndexFunc(a.tabs, func(t remote.Tab) bool { return t.Pane == a.chat.pane })
	if i < 0 {
		return remote.Tab{}, false
	}
	return a.tabs[i], true
}

func (a *App) openChat(pane string) {
	m, ok := a.fleet.Owner(pane)
	if !ok {
		return
	}
	a.chat, a.chatM = newChat(pane), m.ID
	a.links[m.ID].conn.Watch(pane)
}

func (a *App) closeChat() {
	if l := a.links[a.chatM]; l != nil {
		l.conn.Watch("")
	}
	a.chat, a.chatM = chatView{}, ""
}

// many reports whether more than one machine is paired, when rows and
// lines name their machine.
func (a *App) many() bool { return a.fleet != nil && len(a.fleet.Machines) > 1 }

// hosts are the paired machines as the sidebar's host chips take them.
func (a *App) hosts() []sidebar.HostInfo {
	if a.fleet == nil {
		return nil
	}
	var out []sidebar.HostInfo
	for _, m := range a.fleet.Machines {
		l := a.links[m.ID]
		h := sidebar.HostInfo{Name: m.Name, Up: l.online, Note: l.offline, Retry: l.retryAt}
		if !l.online {
			h.Since = l.since
		}
		for _, s := range a.st.Sessions {
			if o, _ := a.fleet.Owner(s.ID); o.ID == m.ID {
				h.Sessions = append(h.Sessions, s.ID)
			}
		}
		out = append(out, h)
	}
	return out
}

// linkOf is the link of the machine that owns id, nil for none.
func (a *App) linkOf(id string) *link {
	if a.fleet == nil {
		return nil
	}
	m, ok := a.fleet.Owner(id)
	if !ok {
		return nil
	}
	return a.links[m.ID]
}

// chatLayout draws the open chat. It draws in real dp, so it undoes the
// zoom.
func (a *App) chatLayout(gtx C) {
	gtx.Metric.PxPerDp /= zoom
	gtx.Metric.PxPerSp /= zoom
	t, open := a.tab()
	l := a.links[a.chatM]
	env := chatEnv{Tab: t, Open: open, Online: l.online, Models: l.hello.Models, Efforts: l.hello.Efforts,
		Retry: l.conn.Retry,
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			a.do(l.m.ID, func(ctx context.Context, c *phone.Client) error { return call(ctx, c) }, done)
		}}
	if a.many() {
		env.Host, env.Hosts = l.m.Name, a.hosts()
	}
	if l.offline != "" {
		env.Offline = offlineText(l.retryAt, gtx.Now)
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second)})
	}
	if a.chat.layout(gtx, a.th, env) {
		a.closeChat()
	}
}

func (a *App) layout(gtx C) {
	switch {
	case a.fleet == nil || a.adding:
		a.pairLayout(gtx)
		return
	case a.machines:
		a.machinesLayout(gtx)
		return
	case a.chat.pane == "" || a.links[a.chatM] == nil:
		a.listLayout(gtx)
		return
	}
	a.chatLayout(gtx)
	// Back, or Esc on a desktop, closes the chat's menu, then its
	// keyboard, then the chat.
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			if !a.chat.handleBack(gtx) {
				a.closeChat()
			}
			a.w.Invalidate()
		}
	}
}
