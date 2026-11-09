package app

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/quanticstudios/sector/procs"
	"github.com/quanticstudios/sector/sample"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

var _ Monitorer = (*FakeBackend)(nil)

// openMonitor opens session's Monitor tab, or shows the one it has, as the
// daemon does for NewSession.Monitor. Callers hold f.mu.
func (f *FakeBackend) openMonitor(session string) {
	session = f.sessionFor(session, "")
	if i := slices.IndexFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.Monitor && w.SessionID == session }); i >= 0 {
		select {
		case f.focus <- proto.FocusSession{WorkspaceID: f.st.Workspaces[i].ID, SessionID: session}:
		default:
		}
		return
	}
	f.nextID++
	f.st.Workspaces = append(f.st.Workspaces, model.Workspace{ID: fmt.Sprintf("mon%d", f.nextID), SessionID: session,
		Label: "Monitor", Path: fakeHome, RepoRoot: fakeHome, UpdatedAt: time.Now(), Monitor: true})
}

// Monitor implements Monitorer with a made-up eight-thread machine, a new
// sample each second.
func (f *FakeBackend) Monitor(string) (proto.MonitorSample, time.Time, bool) {
	at := time.Now().Truncate(time.Second)
	x := float64(at.Unix())
	load := func(i int) float64 { return 0.5 + 0.45*math.Sin(x/7+float64(i)) }
	snap := sample.Snapshot{Time: at,
		Host: sample.Host{Name: "devbox", Model: "AMD Ryzen 7 7700 8-Core Processor", Threads: 8, Uptime: 26 * time.Hour, Load: [3]float64{2.1, 1.8, 1.5}},
		CPU:  sample.CPU{Dies: []sample.Die{{TempC: 61}}, MHz: 4800, PowerW: 54, TempC: 61},
		Mem:  sample.Mem{Total: 32 << 30, Used: 11 << 30, Available: 19 << 30, Cached: 9 << 30, Free: 4 << 30},
		Drives: []sample.Drive{{Name: "nvme0n1", Model: "Samsung SSD 990 PRO 2TB", Mounts: []string{"/"}, Size: 2 << 40, Used: 700 << 30, Free: 1300 << 30,
			ReadBps: 40e6 * load(1), WriteBps: 12e6 * load(2), Busy: 0.1}},
		Nets: []sample.Net{{Name: "enp5s0", Addr: "192.168.1.20", RxBps: 3e6 * load(3), TxBps: 4e5 * load(4)}},
	}
	for i := range 8 {
		snap.CPU.Threads = append(snap.CPU.Threads, load(i))
		snap.CPU.Total += load(i) / 8
	}
	for c := range 4 {
		snap.CPU.Cores = append(snap.CPU.Cores, sample.Core{ID: c, Threads: []int{c, c + 4}})
	}
	claude := procs.Group{Key: "claude 101", Kind: procs.Agent, PID: 101, Name: "claude", Agent: "claude", Repo: "acme-api", Branch: "main",
		Cores: 2.5 * load(0), RSS: 900 << 20, Threads: 30, Procs: []procs.Proc{{PID: 101, Comm: "claude", State: 'S', Cores: 2.5 * load(0), RSS: 900 << 20, Threads: 30}}}
	codex := procs.Group{Key: "codex 202", Kind: procs.Agent, PID: 202, Name: "codex", Agent: "codex", Repo: "web-app",
		Cores: 1.2 * load(5), RSS: 400 << 20, Threads: 12, Procs: []procs.Proc{{PID: 202, Comm: "codex", State: 'S', Cores: 1.2 * load(5), RSS: 400 << 20, Threads: 12}}}
	other := procs.Group{Key: "other", Kind: procs.Other, Name: "Everything else", Cores: 0.4, RSS: 3 << 30, Threads: 400,
		Procs: []procs.Proc{{PID: 1, Comm: "systemd", State: 'S', RSS: 12 << 20, Threads: 1}}}
	t := procs.Table{Groups: []procs.Group{claude, codex, other}, Procs: 312, Threads: 442}
	return proto.MonitorSample{Snapshot: snap, Table: t}, at, true
}
