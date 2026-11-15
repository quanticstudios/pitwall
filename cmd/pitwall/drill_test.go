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

// TestUpdateDrill is the release drill. It builds this pitwall, or under
// nogui this pitwall-core, as v0.0.1 and v0.0.2, serves v0.0.2 as the
// latest release, and starts v0.0.1's daemon in temporary directories with
// an agent TUI, a dev server and a test loop in panes. Then it runs
// v0.0.1's update. In "handoff" all three must keep running under v0.0.2's
// daemon, with their screens and scrollback. In "refused" the release's
// daemon refuses the handoff, and the update must fail naming update
// --restart while v0.0.1's daemon keeps serving and all three keep running.
// In "explicit restart" update --restart must restart the daemon. The
// "back" runs start v0.0.2's daemon and run v0.0.2's update --version
// v0.0.1, an older release that a plain update would not install: it hands
// over, a refusal keeps v0.0.2's daemon, and --restart forces it.
func TestUpdateDrill(t *testing.T) {
	if os.Getenv("PITWALL_TEST_GUARD") != "" {
		// why: go build there gets an empty build cache under the temporary
		// HOME; the drill keeps its own processes in temporary directories.
		t.Skip("inside the guarded run")
	}
	bins := t.TempDir()
	// Model a release without --version at the previous protocol level,
	// than just relabeling this checkout. A refused downgrade's retry then
	// runs an updater that cannot understand the flag we used to install it.
	replace := make(map[string]string)
	for file, change := range map[string]func([]byte) []byte{
		"../../internal/proto/proto.go": func(b []byte) []byte {
			return bytes.Replace(b, fmt.Appendf(nil, "const Level = %d", proto.Level), fmt.Appendf(nil, "const Level = %d", proto.Level-1), 1)
		},
		"update.go": func(b []byte) []byte {
			const signature = "func updateArgs(args []string) (restart bool, tag string, err error) {"
			return bytes.Replace(b, []byte(signature), []byte(signature+`
	for _, arg := range args {
		if arg == "--version" { return false, "", errors.New("usage: pitwall update [--restart]") }
	}`), 1)
		},
	} {
		source, err := filepath.Abs(file)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		changed := change(b)
		if bytes.Equal(b, changed) {
			t.Fatalf("old release fixture did not change %s", file)
		}
		replacement := filepath.Join(bins, filepath.Base(file))
		if err := os.WriteFile(replacement, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		replace[source] = replacement
	}
	data, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(bins, "old-release.json")
	if err := os.WriteFile(overlay, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v0.0.1", "v0.0.2"} {
		var flags []string
		if v == "v0.0.1" {
			flags = []string{"-overlay", overlay}
		}
		buildPitwall(t, filepath.Join(bins, v), v, binName != "pitwall", flags...)
	}
	run := drillRun{daemon: filepath.Join(bins, "v0.0.1"), release: filepath.Join(bins, "v0.0.2"), tag: "v0.0.2", flavor: binName}
	t.Run("handoff", func(t *testing.T) { drill(t, run) })
	run.refuse = true
	t.Run("refused", func(t *testing.T) { drill(t, run) })
	run.restart = true
	t.Run("explicit restart", func(t *testing.T) { drill(t, run) })
	back := drillRun{daemon: run.release, release: run.daemon, tag: "v0.0.1", flavor: binName, version: true}
	t.Run("back", func(t *testing.T) { drill(t, back) })
	back.refuse = true
	t.Run("back refused", func(t *testing.T) { drill(t, back) })
	back.restart = true
	t.Run("back restart", func(t *testing.T) { drill(t, back) })
}

// TestReleaseHandoff runs the last release's own pitwall update, and this
// checkout's pitwall-core update, against a daemon of that release, the way
// users will update to this checkout. The handoff CI job sets
// PITWALL_BASELINE to the release's Linux binary and fails on a skip.
func TestReleaseHandoff(t *testing.T) {
	base := os.Getenv("PITWALL_BASELINE")
	if base == "" {
		t.Skip("PITWALL_BASELINE names no release binary")
	}
	if os.Getenv("PITWALL_TEST_GUARD") != "" {
		t.Skip("inside the guarded run")
	}
	bins := t.TempDir()
	desktop := filepath.Join(bins, "pitwall")
	desktopRefused := filepath.Join(bins, "pitwall-refused")
	core, coreOld := filepath.Join(bins, "pitwall-core"), filepath.Join(bins, "pitwall-core-v0.0.1")
	buildPitwall(t, desktop, "v99.0.0", false)
	buildRefusingPitwall(t, desktopRefused)
	buildPitwall(t, core, "v99.0.0", true)
	buildPitwall(t, coreOld, "v0.0.1", true)
	// a: the release's update installs this desktop build and hands over.
	t.Run("release-update", func(t *testing.T) {
		drill(t, drillRun{daemon: base, release: desktop, tag: "v99.0.0", flavor: "pitwall"})
	})
	// b: this build refuses the handoff; the release's update restarts, as
	// its release notes say.
	t.Run("release-update-refused", func(t *testing.T) {
		drill(t, drillRun{daemon: base, release: desktop, tag: "v99.0.0", flavor: "pitwall", refuse: true, published: true})
	})
	// c: the migration path. This build replaces the release's binary, as
	// get.sh does, and its update meets a refused handoff: the release's
	// daemon keeps serving and every program keeps running.
	t.Run("migration-refused", func(t *testing.T) {
		drill(t, drillRun{daemon: base, updater: desktopRefused, release: desktopRefused, tag: "v99.0.0", flavor: "pitwall", refuse: true})
	})
	// d: pitwall-core's update fetches the core archive and hands the
	// release's daemon over to it.
	t.Run("core-update", func(t *testing.T) {
		drill(t, drillRun{daemon: base, updater: coreOld, release: core, tag: "v99.0.0", flavor: "pitwall-core"})
	})
}

// buildPitwall builds this checkout as version into out; with core, as
// pitwall-core, whatever GOFLAGS says.
func buildPitwall(t *testing.T, out, version string, core bool, flags ...string) {
	t.Helper()
	args := []string{"build", "-o", out, "-ldflags", "-X main.version=" + version}
	if core {
		args = append(args, "-tags", "nogui")
	}
	args = append(args, flags...)
	cmd := exec.Command("go", append(args, ".")...)
	if core {
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", version, err, out)
	}
}

// buildRefusingPitwall builds a candidate with an incompatible handoff
// protocol. A shell wrapper cannot simulate refusal once preinstalled:
// os.Executable inside its exec'd binary bypasses the wrapper.
func buildRefusingPitwall(t *testing.T, out string) {
	t.Helper()
	source, err := filepath.Abs("../../internal/daemon/handoff.go")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	replacement := filepath.Join(dir, "handoff.go")
	changed := regexp.MustCompile(`const handoffVersion = \d+`).ReplaceAll(b, []byte("const handoffVersion = 999"))
	if bytes.Equal(b, changed) {
		t.Fatal("handoff version fixture did not change the protocol")
	}
	if err := os.WriteFile(replacement, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	data, err := json.Marshal(map[string]any{"Replace": map[string]string{source: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, data, 0o600); err != nil {
		t.Fatal(err)
	}
	buildPitwall(t, out, "v99.0.0", false, "-overlay", overlay)
}

// drillRun is one update the drill runs.
type drillRun struct {
	daemon string // a binary whose daemon serves the panes first; it runs the update unless updater is set
	// updater runs the update instead, installed as flavor once the panes
	// run: next to daemon as pitwall-core, or over it as pitwall. When it is
	// release, the latest build is already installed, as after get.sh.
	updater   string
	release   string // the binary served as release tag, in flavor's archive
	tag       string
	flavor    string // pitwall or pitwall-core
	refuse    bool   // the release's daemon refuses the handoff
	restart   bool   // run update --restart
	version   bool   // run update --version tag
	published bool   // the updater is a published release's, which restarts on a refused handoff
}

func drill(t *testing.T, r drillRun) {
	dir := t.TempDir()
	// why: a socket path must fit in 104 bytes on macOS, and $TMPDIR there is long.
	run, err := os.MkdirTemp("/tmp", "pwdrill")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	// why: the daemon rewrites installed agent skills at start; an older
	// release's daemon would write its own over the real ones.
	for k, v := range map[string]string{
		"HOME": filepath.Join(dir, "home"), "XDG_STATE_HOME": filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"CLAUDE_CONFIG_DIR": filepath.Join(dir, "claude"), "CODEX_HOME": filepath.Join(dir, "codex"),
		"PI_CODING_AGENT_DIR": filepath.Join(dir, "pi"), "OPENCODE_CONFIG_DIR": filepath.Join(dir, "opencode"),
		"XDG_RUNTIME_DIR": run, "SHELL": "/bin/sh", "PITWALL_SOCKET": "", "PITWALL_PANE": "",
	} {
		t.Setenv(k, v)
	}
	release := r.release
	refusal := "drill: the release refuses"
	if r.refuse && r.updater == r.release {
		refusal = "handoff request" // preinstalled candidate has another handoffVersion
	} else if r.refuse {
		release = filepath.Join(dir, "refusing")
		script := "#!/bin/sh\nif [ \"$2\" = --handoff ]; then printf 'drill: the release refuses' >&3; exit 1; fi\nexec " + r.release + " \"$@\"\n"
		if err := os.WriteFile(release, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PITWALL_UPDATE_URL", serveRelease(t, r.flavor, r.tag, release)+"/releases/latest")

	// install renames, as get.sh does, so it can replace a running binary.
	install := func(src, name string) string {
		t.Helper()
		bin := filepath.Join(dir, "bin", name)
		os.MkdirAll(filepath.Dir(bin), 0o755)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin+".new", data, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(bin+".new", bin); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	// bin serves the panes until the update, upd runs it and serves them after.
	bin := install(r.daemon, r.flavor)
	if r.updater != "" {
		bin = install(r.daemon, "pitwall")
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
	original, err := doctorDaemon()
	if err != nil {
		t.Fatalf("ask the original daemon its build: %v", err)
	}

	upd := bin
	if r.updater != "" {
		upd = install(r.updater, r.flavor)
	}
	args := []string{"update"}
	if r.restart {
		args = append(args, "--restart")
	}
	if r.version {
		args = append(args, "--version", r.tag)
	}
	out, err := exec.Command(upd, args...).CombinedOutput()
	kept := r.refuse && !r.restart && !r.published // the update keeps the old daemon and fails
	if (err != nil) != kept {
		t.Fatalf("pitwall %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	t.Logf("pitwall %s:\n%s", strings.Join(args, " "), out)
	installed := "Installed " + r.flavor + " " + r.tag + " over "
	if r.updater == r.release {
		installed = "pitwall " + r.tag + " is the latest release."
		if strings.Contains(string(out), "Installed ") {
			t.Error("migration update installed again after the installer had replaced the binary")
		}
	}
	if want := installed; !strings.Contains(string(out), want) {
		t.Errorf("pitwall update did not say %q", want)
	}
	want := "pitwall " + r.tag + "\n"
	if r.flavor == "pitwall-core" {
		want = "pitwall " + r.tag + " (core)\n"
	}
	if v := pitwall(t, upd, dir, "--version"); v != want {
		t.Errorf("the installed %s says %q, want %q", r.flavor, v, want)
	}
	if kept {
		again := r.flavor + " update --restart restarts it"
		if r.version {
			again = r.flavor + " update --restart --version " + r.tag + " restarts it"
		}
		for _, want := range []string{"could not take over (", refusal, "The old daemon keeps serving", again} {
			if !strings.Contains(string(out), want) {
				t.Errorf("pitwall update did not say %q", want)
			}
		}
		if err := syscall.Kill(old.Process.Pid, 0); err != nil {
			t.Errorf("the old daemon (pid %d) stopped: %v", old.Process.Pid, err)
		}
		for name, pid := range map[string]int{"agent": tuiPID, "loop": loopPID, "server": serverPID} {
			if err := syscall.Kill(pid, 0); err != nil {
				t.Errorf("%s (pid %d) stopped in the update: %v", name, pid, err)
			}
		}
		if after := get(t, addr); after <= before {
			t.Errorf("the dev server answered %d after %d", after, before)
		}
		m, err := doctorDaemon()
		if err != nil || m.Build != original.Build {
			t.Fatalf("retained daemon: build %q, err %v; want %q", m.Build, err, original.Build)
		}
		// Use the original executable: the installed one has been replaced.
		// Reading advancing output proves the daemon still serves its panes,
		// rather than merely leaving their processes alive while frozen.
		n := counter(t, r.daemon, dir, "loop", `test run (\d+) ok`)
		until(t, "the retained daemon to read new loop output", func() bool {
			return counter(t, r.daemon, dir, "loop", `test run (\d+) ok`) > n
		})
		until(t, "the retained daemon to read new agent output", func() bool {
			return counter(t, r.daemon, dir, "agent", `turn (\d+)`) > turn
		})
		if !found(t, loopPane, "test run 1 ok") {
			t.Error("the retained daemon lost the loop's scrollback")
		}
		if r.version {
			if !strings.Contains(string(out), "reinstall an updater that supports --version without touching the daemon") {
				t.Fatal("the refused downgrade gave no recovery for a release without --version")
			}
			// The installed old release cannot run the suggested retry. Put a
			// capable updater back, as the install script does without touching
			// the daemon, then request the downgrade and restart together.
			if retry, err := exec.Command(upd, "update", "--restart", "--version", r.tag).CombinedOutput(); err == nil || !bytes.Contains(retry, []byte("usage:")) {
				t.Fatalf("old updater accepted --version: %v\n%s", err, retry)
			}
			install(r.daemon, r.flavor)
			if retry, err := exec.Command(upd, "update", "--restart", "--version", r.tag).CombinedOutput(); err != nil || !bytes.Contains(retry, []byte("The daemon restarted.")) {
				t.Fatalf("forced downgrade after restoring updater: %v\n%s", err, retry)
			}
			until(t, "the forced downgrade to run "+r.tag, func() bool {
				m, err := doctorDaemon()
				return err == nil && m.Build == r.tag && m.Level == proto.Level-1
			})
			until(t, "the forced downgrade to stop the loop", func() bool { return syscall.Kill(loopPID, 0) != nil })
		}
		return
	}
	until(t, "the daemon to run "+r.tag, func() bool {
		m, err := doctorDaemon()
		return err == nil && m.Build == r.tag
	})

	if r.refuse {
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
	n := counter(t, upd, dir, "loop", `test run (\d+) ok`)
	until(t, "the loop to keep running", func() bool { return counter(t, upd, dir, "loop", `test run (\d+) ok`) > n })
	until(t, "the agent to keep drawing", func() bool { return counter(t, upd, dir, "agent", `turn (\d+)`) > turn })
	if screen := pitwall(t, upd, dir, "read", "agent", "--screen"); !strings.Contains(screen, "Claude Code drill") {
		t.Errorf("the agent's screen lost its header:\n%s", screen)
	}
}

// serveRelease serves bin as release tag in flavor's Linux or macOS
// archive, the way GitHub serves releases to update.Check and
// update.Install, and returns the server's URL. Only that archive is
// there, so an update that asks for the other flavor fails.
func serveRelease(t *testing.T, flavor, tag, bin string) string {
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var arc bytes.Buffer
	zw := gzip.NewWriter(&arc)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: flavor, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
	tw.Write(data)
	tw.Close()
	zw.Close()
	name := fmt.Sprintf("%s_%s_%s.tar.gz", flavor, runtime.GOOS, runtime.GOARCH)
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
