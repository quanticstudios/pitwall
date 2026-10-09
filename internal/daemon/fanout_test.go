package daemon

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// emuPane is a Pane over the real emulator, fed by a fake program: a
// spinner line redrawn at 60 Hz, as an agent's status line is, and a new log
// line four times a second, which scrolls the screen.
type emuPane struct {
	mu    sync.Mutex
	e     vt.Emulator
	dirty chan struct{}
	done  chan struct{}
	once  sync.Once
}

func startEmuPane(c pane.Config) *emuPane {
	p := &emuPane{e: c.NewVT(c.Cols, c.Rows, io.Discard), dirty: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		t := time.NewTicker(time.Second / 60)
		defer t.Stop()
		for n := 0; ; n++ {
			select {
			case <-p.done:
				return
			case <-t.C:
			}
			s := fmt.Sprintf("\r\x1b[2K\x1b[33m%c\x1b[0m Working… (%ds · esc to interrupt) · %d tokens", spinner[n%len(spinner)], n/60, n*37)
			if n%15 == 0 {
				s = fmt.Sprintf("\r\x1b[2K\x1b[32m✓\x1b[0m %s step %d: compiled internal/pkg%d in %dms\r\n", c.ID[:4], n/15, n%97, n%500) + s
			}
			p.Write2(s)
		}
	}()
	return p
}

// Write2 is the fake program's output.
func (p *emuPane) Write2(s string) {
	p.mu.Lock()
	p.e.Write([]byte(s))
	p.mu.Unlock()
	select {
	case p.dirty <- struct{}{}:
	default:
	}
}

func (p *emuPane) Write(b []byte) (int, error) { return len(b), nil }
func (p *emuPane) Resize(c, r int) error {
	p.mu.Lock()
	p.e.Resize(c, r)
	p.mu.Unlock()
	return nil
}
func (p *emuPane) Snapshot() vt.Grid { p.mu.Lock(); defer p.mu.Unlock(); return p.e.Snapshot() }
func (p *emuPane) SnapshotAt(off int) vt.Grid {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.e.SnapshotAt(off)
}
func (p *emuPane) ScrollbackLen() int { p.mu.Lock(); defer p.mu.Unlock(); return p.e.ScrollbackLen() }
func (p *emuPane) ScrollbackPushed() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.e.ScrollbackPushed()
}
func (p *emuPane) Modes() vt.Modes        { p.mu.Lock(); defer p.mu.Unlock(); return p.e.Modes() }
func (p *emuPane) Dirty() <-chan struct{} { return p.dirty }
func (p *emuPane) Done() <-chan struct{}  { return p.done }
func (p *emuPane) ExitCode() int          { return 0 }
func (p *emuPane) Cwd() string            { return "" }
func (p *emuPane) Close() error           { p.once.Do(func() { close(p.done) }); return nil }

// tapConn counts and keeps what a client reads.
type tapConn struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *tapConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.mu.Lock()
	c.buf.Write(b[:n])
	c.mu.Unlock()
	return n, err
}

func (c *tapConn) len() int { c.mu.Lock(); defer c.mu.Unlock(); return c.buf.Len() }

// fanoutState is one session of tabs with four panes each.
func fanoutState(panes int) model.State {
	st := model.State{Sessions: []model.Session{{ID: "s", Name: "bench"}}}
	for t := range panes / 4 {
		w := model.Workspace{ID: fmt.Sprintf("w%02d", t), SessionID: "s", ActiveTab: "t"}
		root := &layout.Node{Ratios: []float64{0.25, 0.25, 0.25, 0.25}}
		for i := range 4 {
			id := fmt.Sprintf("p%02d-%d", t, i)
			st.Panes = append(st.Panes, model.Pane{ID: id, WorkspaceID: w.ID})
			root.Children = append(root.Children, &layout.Node{Pane: id})
		}
		w.Tabs = []model.Tab{{ID: "t", Layout: root}}
		st.Workspaces = append(st.Workspaces, w)
	}
	return st
}

// fanoutClient is a GUI that keeps the latest frame of every pane, as
// cmd/pitwall's backend does.
type fanoutClient struct {
	tap    *tapConn
	conn   *proto.Conn
	frames map[string]vt.Grid
	n      atomic.Int64 // frames received
	mark   int          // tap length when measuring started
}

// dialFanout connects a GUI of level that shows tab, telling the daemon so
// when level has View.
func dialFanout(b *testing.B, sock string, level, tab int) *fanoutClient {
	nc, err := net.Dial("unix", sock)
	if err != nil {
		b.Fatal(err)
	}
	c := &fanoutClient{tap: &tapConn{Conn: nc}, frames: map[string]vt.Grid{}}
	c.conn = proto.NewConn(c.tap)
	b.Cleanup(func() { c.conn.Close() })
	if err := c.conn.Send(proto.Hello{Version: proto.Version, Level: level, Kind: "gui", Session: "bench"}); err != nil {
		b.Fatal(err)
	}
	if level >= proto.Since(proto.View{}) {
		v := proto.View{}
		for i := range 4 {
			v.Panes = append(v.Panes, fmt.Sprintf("p%02d-%d", tab, i))
		}
		if err := c.conn.Send(v); err != nil {
			b.Fatal(err)
		}
	}
	go func() {
		for {
			m, err := c.conn.Recv()
			if err != nil {
				return
			}
			if f, ok := m.(proto.Frame); ok {
				c.frames[f.Pane] = f.Grid
				c.n.Add(1)
			}
		}
	}()
	return c
}

// cpuSeconds is the process's CPU time in Go code and GC so far. The
// runtime updates it at each GC, so it runs one.
func cpuSeconds() float64 {
	runtime.GC()
	s := []metrics.Sample{{Name: "/cpu/classes/user:cpu-seconds"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}}
	metrics.Read(s)
	return s[0].Value.Float64() + s[1].Value.Float64()
}

// replay decodes the bytes c read after mark, timing that part, and then
// encodes the same messages again, as the daemon did.
func (c *fanoutClient) replay(b *testing.B) (decode, encode time.Duration) {
	c.tap.mu.Lock()
	data := bytes.Clone(c.tap.buf.Bytes())
	c.tap.mu.Unlock()
	r := &readConn{r: bytes.NewReader(data)}
	dec := proto.NewConn(r)
	var msgs []any
	for {
		at := len(data) - r.r.Len()
		start := time.Now()
		m, err := dec.Recv()
		if err != nil {
			break
		}
		if at >= c.mark {
			decode += time.Since(start)
			msgs = append(msgs, m)
		}
	}
	enc := proto.NewConn(&readConn{r: bytes.NewReader(nil)})
	start := time.Now()
	for _, m := range msgs {
		if err := enc.Send(m); err != nil {
			b.Fatal(err)
		}
	}
	return decode, time.Since(start)
}

// readConn reads from r and drops writes.
type readConn struct {
	net.Conn
	r *bytes.Reader
}

func (c *readConn) Read(b []byte) (int, error)  { return c.r.Read(b) }
func (c *readConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *readConn) Close() error                { return nil }

// BenchmarkFanout runs a daemon with N busy panes, four to a tab, and one
// or two GUIs that each show one tab, and reports what reaches the GUIs:
// MB/s and frames/s, the CPU of the whole process (daemon and GUIs, as
// cpu-%), and the time spent encoding (daemon) and decoding (GUIs) the
// frames, in ms per second of wall time. One op is 10ms of wall time.
// GUIs of level 13 predate View and get every pane's frames.
func BenchmarkFanout(b *testing.B) {
	for _, level := range []int{13, proto.Level} {
		for _, panes := range []int{8, 16, 32} {
			for _, guis := range []int{1, 2} {
				b.Run(fmt.Sprintf("level=%d/panes=%d/guis=%d", level, panes, guis), func(b *testing.B) {
					benchFanout(b, panes, guis, level)
				})
			}
		}
	}
}

func benchFanout(b *testing.B, panes, guis, level int) {
	log.SetOutput(io.Discard) // a line per pane started and resized
	defer log.SetOutput(os.Stderr)
	f := &fakes{statsCalls: map[string]int{}, saved: fanoutState(panes)}
	sock, stop := runWith(b, f, func(o *Options) {
		o.NewVT = vt.New
		o.StartPane = func(c pane.Config) (Pane, error) { return startEmuPane(c), nil }
	})
	defer stop()
	var cs []*fanoutClient
	for i := range guis {
		cs = append(cs, dialFanout(b, sock, level, i))
	}
	for t := range panes / 4 {
		for i := range 4 {
			if err := cs[0].conn.Send(proto.Resize{Pane: fmt.Sprintf("p%02d-%d", t, i), Cols: 100, Rows: 30}); err != nil {
				b.Fatal(err)
			}
		}
	}
	time.Sleep(300 * time.Millisecond) // the first frames and the resizes settle
	for _, c := range cs {
		c.mark = c.tap.len()
		c.n.Store(0)
	}
	cpu := cpuSeconds()
	b.ResetTimer()
	start := time.Now()
	time.Sleep(time.Duration(b.N) * 10 * time.Millisecond)
	wall := time.Since(start).Seconds()
	cpu = cpuSeconds() - cpu
	b.StopTimer()
	var bytes, frames int
	var dec, enc time.Duration
	for _, c := range cs {
		bytes += c.tap.len() - c.mark
		frames += int(c.n.Load())
		d, e := c.replay(b)
		dec, enc = dec+d, enc+e
	}
	b.ReportMetric(float64(bytes)/wall/1e6, "MB/s")
	b.ReportMetric(float64(frames)/wall, "frames/s")
	b.ReportMetric(100*cpu/wall, "cpu-%")
	b.ReportMetric(float64(enc.Milliseconds())/wall, "encode-ms/s")
	b.ReportMetric(float64(dec.Milliseconds())/wall, "decode-ms/s")
	b.ReportMetric(0, "ns/op")
}
