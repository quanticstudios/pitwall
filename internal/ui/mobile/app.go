// Package mobile is pitwall's phone app: the sidebar's tree of a
// computer's tabs, each agent tab's chat, and pairing, over the phone
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

	client  *phone.Client // nil while unpaired
	conn    *phone.Conn
	online  bool
	offline string // why the stream dropped; "" while online
	hello   phone.Hello
	focused bool

	tabs    []remote.Tab
	st      model.State
	session string // the State session the list shows
	sb      sidebar.Sidebar

	pair  pairView
	list  listView
	chat  chatView
	sheet sheetView
}

// Run runs the app in w, with its pairing kept in dir, until w closes.
func Run(w *app.Window, th *theme.Theme, dir string) error {
	a := &App{w: w, th: th, dir: dir, ui: make(chan func(), 256)}
	a.sb.TreeOnly, a.sb.Fill = true, true
	a.pair.init()
	if c, err := phone.Load(dir); err == nil {
		a.start(c)
	}
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			if a.conn != nil {
				a.conn.Stop()
			}
			return e.Err
		case app.ConfigEvent:
			// why: back in the foreground, a stream from before the phone slept may be dead without an error.
			if e.Config.Focused && !a.focused && a.conn != nil {
				a.conn.Retry()
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

// do runs call off the window's goroutine and then done on it, with
// call's error; ErrUnpaired unpairs.
func (a *App) do(call func(ctx context.Context, c *phone.Client) error, done func(error)) {
	c := a.client
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := call(ctx, c)
		a.post(func() {
			if errors.Is(err, phone.ErrUnpaired) && a.client == c {
				a.unpair(err.Error())
				return
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// start opens the stream with c's pairing.
func (a *App) start(c phone.Creds) {
	a.client = phone.NewClient(c)
	a.online, a.offline = false, ""
	cl := a.client
	a.conn = &phone.Conn{Client: a.client,
		On: func(e phone.Event) { a.post(func() { a.event(cl, e) }) },
		Status: func(on bool, err error) {
			a.post(func() {
				if a.client != cl {
					return
				}
				a.online, a.offline = on, ""
				if errors.Is(err, phone.ErrUnpaired) {
					a.unpair(err.Error())
				} else if err != nil {
					a.offline = err.Error()
				}
			})
		}}
	a.conn.Start(a.chat.pane)
}

// unpair forgets the pairing and goes back to the pairing screen.
func (a *App) unpair(why string) {
	if a.conn != nil {
		a.conn.Stop()
	}
	phone.Forget(a.dir)
	a.client, a.conn, a.tabs, a.st, a.session = nil, nil, nil, model.State{}, ""
	a.chat = chatView{}
	a.sheet = sheetView{}
	a.pair.note, a.pair.bad = why, why != ""
}

func (a *App) event(c *phone.Client, e phone.Event) {
	if a.client != c {
		return
	}
	switch e.Name {
	case "hello":
		a.hello = e.Hello
	case "tabs":
		a.tabs, a.st = e.Tabs, phone.State(e.Tabs)
		if a.st.Session(a.session) == nil {
			a.session = ""
			if len(a.st.Sessions) > 0 {
				a.session = a.st.Sessions[0].ID
			}
		}
		if t, ok := a.tab(); ok {
			a.chat.data.Update(t, time.Now())
		}
	case "reset":
		if e.Pane == a.chat.pane && a.chat.pane != "" {
			a.chat.reset(e.Entries)
		}
	case "entries":
		if e.Pane == a.chat.pane && a.chat.pane != "" {
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
	a.chat = newChat(pane)
	a.conn.Watch(pane)
}

func (a *App) closeChat() {
	a.chat = chatView{}
	a.sheet = sheetView{}
	a.conn.Watch("")
}

// chatLayout draws the open chat. It draws in real dp, so it undoes the
// zoom.
func (a *App) chatLayout(gtx C) {
	gtx.Metric.PxPerDp /= zoom
	gtx.Metric.PxPerSp /= zoom
	t, open := a.tab()
	env := chatEnv{Tab: t, Open: open, Online: a.online, Models: a.hello.Models, Efforts: a.hello.Efforts,
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			a.do(func(ctx context.Context, c *phone.Client) error { return call(ctx, c) }, done)
		}}
	if a.offline != "" {
		env.Offline = "Offline, reconnecting"
	}
	if a.conn != nil {
		env.Retry = a.conn.Retry
	}
	if a.chat.layout(gtx, a.th, env) {
		a.closeChat()
	}
}

func (a *App) layout(gtx C) {
	switch {
	case a.client == nil:
		a.pairLayout(gtx)
	case a.chat.pane == "":
		a.listLayout(gtx)
	default:
		a.chatLayout(gtx)
	}
	a.sheetLayout(gtx)
	// Back, or Esc on a desktop, closes the sheet, then the chat. On the
	// list it goes unread, so Android leaves the app.
	if a.sheet.open == "" && a.chat.pane == "" {
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			if a.sheet.open != "" {
				a.sheet = sheetView{}
			} else {
				a.closeChat()
			}
			a.w.Invalidate()
		}
	}
}
