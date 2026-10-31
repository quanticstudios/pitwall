//go:build unix

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestUpdateDrill is the release drill. It builds this pitwall as v0.0.1 and
// v0.0.2, serves v0.0.2 as the latest release, and starts v0.0.1's daemon
// in temporary directories with an agent TUI, a dev server and a test loop
// in panes. Then it runs v0.0.1's pitwall update. In "handoff" all three
// must keep running under v0.0.2's daemon, with their screens and
// scrollback. In "restart" the release's daemon refuses the handoff, and
// the update must say so and restart the daemon.
func TestUpdateDrill(t *testing.T) {
	if os.Getenv("PITWALL_TEST_GUARD") != "" {
		// why: go build there gets an empty build cache under the temporary
		// HOME; the drill keeps its own processes in temporary directories.
		t.Skip("inside the guarded run")
	}
	bins := t.TempDir()
	for _, v := range []string{"v0.0.1", "v0.0.2"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bins, v), "-ldflags", "-X main.version="+v, ".").CombinedOutput()
		if err != nil {
			t.Fatalf("go build %s: %v\n%s", v, err, out)
		}
	}
	t.Run("handoff", func(t *testing.T) { drill(t, bins, false) })
	t.Run("restart", func(t *testing.T) { drill(t, bins, true) })
}

func drill(t *testing.T, bins string, refuse bool) {
	dir := t.TempDir()
	// why: a socket path must fit in 104 bytes on macOS, and $TMPDIR there is long.
	run, err := os.MkdirTemp("/tmp", "pwdrill")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	for k, v := range map[string]string{
		"HOME": filepath.Join(dir, "home"), "XDG_STATE_HOME": filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"XDG_RUNTIME_DIR": run, "SHELL": "/bin/sh", "PITWALL_SOCKET": "", "PITWALL_PANE": "",
	} {
		t.Setenv(k, v)
	}
	release := filepath.Join(bins, "v0.0.2")
	if refuse {
		release = filepath.Join(dir, "refusing")
		script := "#!/bin/sh\nif [ \"$2\" = --handoff ]; then printf 'drill: the release refuses' >&3; exit 1; fi\nexec " + filepath.Join(bins, "v0.0.2") + " \"$@\"\n"
		if err := os.WriteFile(release, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PITWALL_UPDATE_URL", serveRelease(t, "v0.0.2", release)+"/releases/latest")

	bin := filepath.Join(dir, "bin", "pitwall")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	data, err := os.ReadFile(filepath.Join(bins, "v0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	daemonLog, err := os.Create(filepath.Join(dir, "daemon.out"))
	if err != nil {
		t.Fatal(err)
	}
	defer daemonLog.Close()
	old := exec.Command(bin, "daemon")
	old.Stdout, old.Stderr = daemonLog, daemonLog
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	go old.Wait()
	var pids []int // every process to end: the daemons, then the panes'
	t.Cleanup(func() {
		path, _ := proto.SocketPath()
		if b, err := os.ReadFile(path + ".lock"); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pids = append([]int{pid}, pids...)
			}
		}
		pids = append([]int{old.Process.Pid}, pids...)
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGTERM)
		}
		// The daemons save their state on the way out, into dir.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if syscall.Kill(pids[0], 0) != nil && syscall.Kill(pids[1], 0) != nil {
				break
			}
		}
		if t.Failed() {
			logs, _ := os.ReadFile(filepath.Join(dir, "state", "pitwall", "daemon.log"))
			crash, _ := os.ReadFile(filepath.Join(dir, "state", "pitwall", "crash.log"))
			t.Logf("daemon.log:\n%s\ncrash.log:\n%s", logs, crash)
		}
	})
	until(t, "the daemon to listen", func() bool {
		path, _ := proto.SocketPath()
		c, err := net.Dial("unix", path)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	tui := `echo $$ > tui.pid; printf '\033[?1049h\033[H\033[1mClaude Code\033[0m drill\r\n'; i=0; while :; do i=$((i+1)); printf '\033[3;1Hturn %d ' $i; sleep 0.1; done`
	loop := `echo $$ > loop.pid; i=0; while :; do i=$((i+1)); echo "test run $i ok"; sleep 0.05; done`
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	server := "exec env PITWALL_DRILL_SERVE=server.addr " + self
	for name, script := range map[string]string{"agent": tui, "loop": loop, "server": server} {
		pitwall(t, bin, dir, "new", "-d", "-n", name, "--", "sh", "-c", script)
	}
	tuiPID, loopPID := pidFile(t, dir, "tui.pid"), pidFile(t, dir, "loop.pid")
	addr, serverPID := serverFile(t, dir)
	pids = append(pids, tuiPID, loopPID, serverPID)
	loopPane := drillPane(t, bin, dir, "loop")
	until(t, "the loop to scroll", func() bool { return found(t, loopPane, "test run 80 ok") })
	if !found(t, loopPane, "test run 1 ok") {
		t.Fatal("the loop's first line is not in its scrollback before the update")
	}
	before := get(t, addr)
	turn := counter(t, bin, dir, "agent", `turn (\d+)`)

	out, err := exec.Command(bin, "update").CombinedOutput()
	if err != nil {
		t.Fatalf("pitwall update: %v\n%s", err, out)
	}
	t.Logf("pitwall update:\n%s", out)
	if !strings.Contains(string(out), "Installed pitwall v0.0.2 over v0.0.1.") {
		t.Error("pitwall update did not say it installed v0.0.2")
	}
	until(t, "the daemon to run v0.0.2", func() bool {
		m, err := doctorDaemon()
		return err == nil && m.Build == "v0.0.2"
	})

	if refuse {
		for _, want := range []string{"could not take over (drill: the release refuses)", "The daemon restarted."} {
			if !strings.Contains(string(out), want) {
				t.Errorf("pitwall update did not say %q", want)
			}
		}
		until(t, "the restart to end the loop", func() bool { return syscall.Kill(loopPID, 0) != nil })
		return
	}
	if !strings.Contains(string(out), "The new daemon took over; every program in a pane kept running.") {
		t.Error("pitwall update did not say the new daemon took over")
	}
	for name, pid := range map[string]int{"agent": tuiPID, "loop": loopPID, "server": serverPID} {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("%s (pid %d) stopped in the update: %v", name, pid, err)
		}
	}
	if pidFile(t, dir, "loop.pid") != loopPID {
		t.Error("the loop started again")
	}
	if after := get(t, addr); after <= before {
		t.Errorf("the dev server answered %d after %d", after, before)
	}
	if !found(t, loopPane, "test run 1 ok") {
		t.Error("the loop's first line left its scrollback in the update")
	}
	n := counter(t, bin, dir, "loop", `test run (\d+) ok`)
	until(t, "the loop to keep running", func() bool { return counter(t, bin, dir, "loop", `test run (\d+) ok`) > n })
	until(t, "the agent to keep drawing", func() bool { return counter(t, bin, dir, "agent", `turn (\d+)`) > turn })
	if screen := pitwall(t, bin, dir, "read", "agent", "--screen"); !strings.Contains(screen, "Claude Code drill") {
		t.Errorf("the agent's screen lost its header:\n%s", screen)
	}
}

// serveRelease serves bin as release tag, the way GitHub serves releases
// to update.Check and update.Install, and returns the server's URL.
func serveRelease(t *testing.T, tag, bin string) string {
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var arc bytes.Buffer
	zw := gzip.NewWriter(&arc)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "pitwall", Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
	tw.Write(data)
	tw.Close()
	zw.Close()
	name := fmt.Sprintf("pitwall_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(arc.Bytes())
	files := map[string][]byte{
		"/releases/download/" + tag + "/" + name:       arc.Bytes(),
		"/releases/download/" + tag + "/checksums.txt": fmt.Appendf(nil, "%x  %s\n", sum, name),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
			return
		}
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// serveDrill is the drill's dev server: it writes its address and pid to
// file, then answers each request with how many it has answered.
func serveDrill(file string) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println(err)
		return 1
	}
	if err := os.WriteFile(file, fmt.Appendf(nil, "%s %d", ln.Addr(), os.Getpid()), 0o600); err != nil {
		fmt.Println(err)
		return 1
	}
	n := 0
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Printf("GET %s %d\n", r.URL.Path, n)
		fmt.Fprint(w, n)
	}))
	return 1
}

// pitwall runs bin with args in dir and returns what it prints.
func pitwall(t *testing.T, bin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pitwall %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// drillPane is the id of tab title's pane.
func drillPane(t *testing.T, bin, dir, title string) string {
	t.Helper()
	var tabs []struct {
		Title string
		Panes []struct{ ID string } `json:"pane_list"`
	}
	if err := json.Unmarshal([]byte(pitwall(t, bin, dir, "ls", "--json")), &tabs); err != nil {
		t.Fatal(err)
	}
	for _, tab := range tabs {
		if tab.Title == title && len(tab.Panes) > 0 {
			return tab.Panes[0].ID
		}
	}
	t.Fatalf("no tab %s", title)
	return ""
}

// found reports whether pane's scrollback or screen holds query.
func found(t *testing.T, pane, query string) bool {
	t.Helper()
	conn, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Send(proto.Search{Pane: pane, Query: query}); err != nil {
		t.Fatal(err)
	}
	for {
		m, err := conn.Recv()
		if err != nil {
			t.Fatal(err)
		}
		switch m := m.(type) {
		case proto.SearchResult:
			return len(m.Matches) > 0
		case proto.Error:
			t.Fatal(m.Message)
		}
	}
}

// counter is the last number re matches on tab's screen, 0 for none.
func counter(t *testing.T, bin, dir, tab, re string) int {
	t.Helper()
	ms := regexp.MustCompile(re).FindAllStringSubmatch(pitwall(t, bin, dir, "read", tab, "--screen"), -1)
	if len(ms) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(ms[len(ms)-1][1])
	return n
}

func get(t *testing.T, addr string) int {
	t.Helper()
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	n, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("dev server: %q", b)
	}
	return n
}

func pidFile(t *testing.T, dir, name string) int {
	t.Helper()
	var pid int
	until(t, name, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return pid > 0
	})
	return pid
}

func serverFile(t *testing.T, dir string) (addr string, pid int) {
	t.Helper()
	until(t, "the dev server", func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "server.addr"))
		_, err := fmt.Sscan(string(b), &addr, &pid)
		return err == nil
	})
	return addr, pid
}

// until waits up to 20 seconds for ok.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
