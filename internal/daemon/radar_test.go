package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// gitT runs git in dir with a fixed identity and no hooks.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Radar Test", "-c", "user.email=radar@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// radarRepo is a repo on main with a.txt, c.txt and z.txt of five lines
// each, and a worktree on a branch of main for each name, by name.
func radarRepo(t *testing.T, names ...string) map[string]string {
	t.Helper()
	root := t.TempDir()
	gitT(t, root, "init", "--quiet", "--initial-branch=main")
	for _, f := range []string{"a.txt", "c.txt", "z.txt"} {
		setLine(t, root, f, -1, "")
	}
	gitT(t, root, "add", "--all")
	gitT(t, root, "commit", "--quiet", "-m", "start")
	dirs := map[string]string{}
	for _, n := range names {
		dirs[n] = filepath.Join(t.TempDir(), n)
		gitT(t, root, "worktree", "add", "--quiet", "-b", n, dirs[n], "main")
	}
	return dirs
}

// setLine writes file in dir as lines 1 to 5 with line i (from 0) replaced
// by text; i -1 replaces none.
func setLine(t *testing.T, dir, file string, i int, text string) {
	t.Helper()
	lines := []string{"1", "2", "3", "4", "5"}
	if i >= 0 {
		lines[i] = text
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitT(t, dir, "add", "--all")
	gitT(t, dir, "commit", "--quiet", "-m", "work")
}

// radarDaemon is a daemon with a named tab per worktree, on its branch, and
// the radar on.
func radarDaemon(dirs map[string]string) *Daemon {
	d := &Daemon{o: Options{Radar: func() bool { return true }, Changes: gitstat.Changed, Conflicts: gitstat.Conflicts,
		Save: func(model.State) error { return nil }}}
	for _, n := range []string{"a", "b", "c"} {
		if dir, ok := dirs[n]; ok {
			d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: n, Name: n, NameSet: true, Branch: n, Path: dir})
		}
	}
	return d
}

// TestRadarScan checks the radar against temp repos: branches touching
// other files, the same file apart, the same lines, uncommitted, and three
// branches at once.
func TestRadarScan(t *testing.T) {
	type o = model.Overlap
	for _, c := range []struct {
		name  string
		work  func(t *testing.T, dirs map[string]string)
		names []string
		want  map[string][]model.Overlap
	}{
		{"no overlap", func(t *testing.T, dirs map[string]string) {
			setLine(t, dirs["a"], "a.txt", 0, "a")
			commitAll(t, dirs["a"])
			setLine(t, dirs["b"], "c.txt", 0, "b")
			commitAll(t, dirs["b"])
		}, []string{"a", "b"}, nil},
		{"overlap without conflict", func(t *testing.T, dirs map[string]string) {
			setLine(t, dirs["a"], "c.txt", 0, "a")
			commitAll(t, dirs["a"])
			setLine(t, dirs["b"], "c.txt", 4, "b")
			commitAll(t, dirs["b"])
		}, []string{"a", "b"}, map[string][]o{
			"a": {{WorkspaceID: "b", Branch: "b", Files: []string{"c.txt"}}},
			"b": {{WorkspaceID: "a", Branch: "a", Files: []string{"c.txt"}}},
		}},
		{"conflict, listed first", func(t *testing.T, dirs map[string]string) {
			for _, n := range []string{"a", "b"} {
				setLine(t, dirs[n], "z.txt", 2, n) // the same line: a conflict
				setLine(t, dirs[n], "c.txt", map[string]int{"a": 0, "b": 4}[n], n)
				commitAll(t, dirs[n])
			}
		}, []string{"a", "b"}, map[string][]o{
			"a": {{WorkspaceID: "b", Branch: "b", Files: []string{"z.txt", "c.txt"}, Conflicts: 1}},
			"b": {{WorkspaceID: "a", Branch: "a", Files: []string{"z.txt", "c.txt"}, Conflicts: 1}},
		}},
		{"uncommitted overlap", func(t *testing.T, dirs map[string]string) {
			setLine(t, dirs["a"], "a.txt", 0, "a")
			commitAll(t, dirs["a"])
			setLine(t, dirs["b"], "a.txt", 0, "b") // would conflict, once committed
		}, []string{"a", "b"}, map[string][]o{
			"a": {{WorkspaceID: "b", Branch: "b", Files: []string{"a.txt"}}},
			"b": {{WorkspaceID: "a", Branch: "a", Files: []string{"a.txt"}}},
		}},
		{"three branches", func(t *testing.T, dirs map[string]string) {
			setLine(t, dirs["a"], "a.txt", 0, "a")
			setLine(t, dirs["b"], "a.txt", 0, "b")
			setLine(t, dirs["b"], "c.txt", 0, "b")
			setLine(t, dirs["c"], "c.txt", 4, "c")
			for _, n := range []string{"a", "b", "c"} {
				commitAll(t, dirs[n])
			}
		}, []string{"a", "b", "c"}, map[string][]o{
			"a": {{WorkspaceID: "b", Branch: "b", Files: []string{"a.txt"}, Conflicts: 1}},
			"b": {{WorkspaceID: "a", Branch: "a", Files: []string{"a.txt"}, Conflicts: 1}, {WorkspaceID: "c", Branch: "c", Files: []string{"c.txt"}}},
			"c": {{WorkspaceID: "b", Branch: "b", Files: []string{"c.txt"}}},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dirs := radarRepo(t, c.names...)
			c.work(t, dirs)
			d := radarDaemon(dirs)
			d.scanRadar(context.Background())
			if !reflect.DeepEqual(d.st.Overlaps, c.want) {
				t.Fatalf("Overlaps = %+v\nwant %+v", d.st.Overlaps, c.want)
			}
		})
	}
}

// fakeRadar is Changes and Conflicts from fixed answers by worktree, which
// all share one repository.
type fakeRadar struct {
	mu        sync.Mutex
	changes   map[string]gitstat.Changes
	conflicts []string
	calls     atomic.Int32 // Changes
	scans     atomic.Int32 // the setting, read once a scan
	block     bool         // Changes waits for its context to end
}

func (f *fakeRadar) Changes(ctx context.Context, dir string) (gitstat.Changes, error) {
	f.calls.Add(1)
	if f.block {
		<-ctx.Done()
		return gitstat.Changes{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.changes[dir]
	c.Common, c.Root = "/repo/.git", dir
	if c.Head == "" {
		c.Head = "head-" + dir
	}
	return c, nil
}

func (f *fakeRadar) Conflicts(context.Context, string, string, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conflicts, nil
}

func (f *fakeRadar) daemon() *Daemon {
	d := &Daemon{o: Options{Radar: func() bool { f.scans.Add(1); return true }, Changes: f.Changes, Conflicts: f.Conflicts,
		Save: func(model.State) error { return nil }}}
	for _, n := range []string{"billing", "acme-api"} {
		d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: n, Name: n, NameSet: true, Branch: n, Path: "/wt/" + n})
	}
	return d
}

// TestRadarNotice checks the radar tells about a pair and file once,
// again only for a new file or a new conflict, and waits while another
// notice shows.
func TestRadarNotice(t *testing.T) {
	f := &fakeRadar{changes: map[string]gitstat.Changes{
		"/wt/billing":  {Uncommitted: []string{"src/server/router.ts"}},
		"/wt/acme-api": {Uncommitted: []string{"src/server/router.ts"}},
	}}
	d := f.daemon()
	scan := func(want string) {
		t.Helper()
		d.radar.last = "" // the fakes changed what the stats would not show
		d.scanRadar(context.Background())
		if d.st.Notice != want {
			t.Fatalf("Notice = %q, want %q", d.st.Notice, want)
		}
	}
	scan("acme-api and billing both edit src/server/router.ts.")
	d.st.Notice = "" // dismissed
	scan("")

	f.changes["/wt/billing"] = gitstat.Changes{Committed: []string{"go.mod"}, Uncommitted: []string{"src/server/router.ts"}}
	f.changes["/wt/acme-api"] = gitstat.Changes{Committed: []string{"go.mod"}, Uncommitted: []string{"src/server/router.ts"}}
	d.st.Notice = "Restored 2 tabs."
	scan("Restored 2 tabs.") // held back, not dropped
	d.st.Notice = ""
	scan("acme-api and billing both edit go.mod.")
	scan("acme-api and billing both edit go.mod.") // its own notice stays

	f.conflicts = []string{"go.mod"}
	f.changes["/wt/billing"] = gitstat.Changes{Head: "billing-2", Committed: []string{"go.mod"}, Uncommitted: []string{"src/server/router.ts"}}
	d.st.Notice = ""
	scan("acme-api and billing would conflict in go.mod.")
	d.st.Notice = ""
	scan("")
}

// TestRadarSameBranch checks two tabs on one branch, such as a shell and
// an agent in one worktree, never warn about each other.
func TestRadarSameBranch(t *testing.T) {
	f := &fakeRadar{changes: map[string]gitstat.Changes{"/wt/billing": {Committed: []string{"go.mod"}}}}
	d := f.daemon()
	d.st.Workspaces[1] = model.Workspace{ID: "shell", Branch: "billing", Path: "/wt/billing"}
	d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: "main", Branch: "main", Path: "/wt/main"})
	d.scanRadar(context.Background())
	if d.st.Overlaps != nil || d.st.Notice != "" {
		t.Fatalf("Overlaps = %+v, Notice %q", d.st.Overlaps, d.st.Notice)
	}
}

// TestRadarDebounce checks a burst of kicks makes one scan, after
// radarDelay.
func TestRadarDebounce(t *testing.T) {
	defer func(d time.Duration) { radarDelay = d }(radarDelay)
	radarDelay = 50 * time.Millisecond
	f := &fakeRadar{}
	d := f.daemon()
	for range 20 {
		d.kickRadar()
	}
	if n := f.scans.Load(); n != 0 {
		t.Fatalf("%d scans before radarDelay", n)
	}
	time.Sleep(300 * time.Millisecond)
	if s, n := f.scans.Load(), f.calls.Load(); s != 1 || n != 2 {
		t.Fatalf("a burst of kicks: %d scans, %d reads; want 1 scan reading both tabs", s, n)
	}
	d.kickRadar()
	time.Sleep(300 * time.Millisecond)
	if s, n := f.scans.Load(), f.calls.Load(); s != 2 || n != 2 {
		t.Fatalf("%d scans, %d reads; want a scan that reads nothing when nothing changed", s, n)
	}
}

// TestRadarBudget checks a scan gives up after radarBudget, keeps the
// last result, and runs again next time.
func TestRadarBudget(t *testing.T) {
	defer func(d time.Duration) { radarBudget = d }(radarBudget)
	radarBudget = 50 * time.Millisecond
	f := &fakeRadar{block: true}
	d := f.daemon()
	old := map[string][]model.Overlap{"billing": {{WorkspaceID: "acme-api", Files: []string{"x"}}}}
	d.st.Overlaps = old
	start := time.Now()
	d.scanRadar(context.Background())
	if took := time.Since(start); took > time.Second {
		t.Fatalf("scan took %v with a %v budget", took, radarBudget)
	}
	if !reflect.DeepEqual(d.st.Overlaps, old) {
		t.Fatalf("Overlaps = %+v, want the last result kept", d.st.Overlaps)
	}
	f.block = false
	d.scanRadar(context.Background())
	if n := f.calls.Load(); n != 3 || d.st.Overlaps != nil {
		t.Fatalf("after the budget: %d reads, Overlaps %+v; want a fresh scan", n, d.st.Overlaps)
	}
}
