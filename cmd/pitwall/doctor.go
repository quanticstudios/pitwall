package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// runDoctor is `pitwall doctor`: one ok, FAIL or info line per check, each
// FAIL followed by the command that fixes it. It changes nothing and
// returns 1 when any check failed.
func runDoctor(w io.Writer) int {
	// Adapted from forge (MIT): cmd/forge/ops.go
	failed := false
	report := func(ok bool, what, detail, fix string) {
		mark := "ok  "
		if !ok {
			mark, failed = "FAIL", true
		}
		if detail != "" {
			what += ": " + detail
		}
		fmt.Fprintf(w, "%s %s\n", mark, what)
		if !ok && fix != "" {
			fmt.Fprintf(w, "     fix: %s\n", fix)
		}
	}
	info := func(what string) { fmt.Fprintf(w, "info %s\n", what) }
	home, _ := os.UserHomeDir()
	short := func(path string) string {
		if rest, ok := strings.CutPrefix(path, home+string(os.PathSeparator)); ok && home != "" {
			return "~/" + filepath.ToSlash(rest)
		}
		return path
	}

	info("pitwall " + versionString())
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	onPath, pathErr := exec.LookPath("pitwall")
	switch {
	case err != nil:
		report(false, "this pitwall", err.Error(), "")
	case pathErr != nil:
		report(false, "pitwall on PATH", "not found", "add "+filepath.Dir(self)+" to PATH")
	default:
		a, errA := os.Stat(self)
		b, errB := os.Stat(onPath)
		same, detail := errA == nil && errB == nil && os.SameFile(a, b), onPath
		if !same {
			detail += ", not this pitwall " + self
		}
		report(same, "pitwall on PATH", detail, "put "+filepath.Dir(self)+" first on PATH, or remove "+onPath+"; agents run the pitwall on PATH")
	}

	m, err := doctorDaemon()
	ok, detail, fix := daemonRow(m, err)
	if errors.Is(err, errNotRunning) {
		info("daemon not running; the window starts it")
	} else {
		report(ok, "daemon", detail, fix)
	}

	hooksRows(home, short, report)

	s, probs := config.Load()
	for _, p := range probs {
		report(false, "config", fmt.Sprint(p), "edit "+short(config.Path()))
	}
	if len(probs) == 0 {
		report(true, "config", short(config.Path()), "")
	}
	for _, n := range s.Notes {
		info(fmt.Sprint(n))
	}

	// docs/remote.md: without linger, logging out deletes the runtime folder
	// that holds the socket.
	if runtime.GOOS == "linux" && os.Getenv("SSH_CONNECTION") != "" {
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			if u, err := user.Current(); err == nil {
				_, err := os.Stat(filepath.Join("/var/lib/systemd/linger", u.Username))
				report(err == nil, "systemd linger", u.Username, "loginctl enable-linger")
			}
		}
	}
	if failed {
		return 1
	}
	return 0
}

// doctorDaemon asks the daemon for its state, as `pitwall ls` does.
func doctorDaemon() (proto.StateMsg, error) {
	conn, err := dialCLI()
	if err != nil {
		return proto.StateMsg{}, err
	}
	defer conn.Close()
	return syncMsg(conn)
}

// daemonRow judges the daemon's answer: it must run this build at this
// protocol Level.
func daemonRow(m proto.StateMsg, err error) (ok bool, detail, fix string) {
	const restart = "run pitwall and choose Restart now, or click Restart to finish; programs in panes stop"
	if err != nil {
		if v, refused := proto.RefusedVersion(proto.Error{Message: err.Error()}); refused {
			return false, fmt.Sprintf("speaks protocol %d, this pitwall %d", v, proto.Version), restart
		}
		return false, err.Error(), "read daemon.log and crash.log; pitwall logs prints where they are"
	}
	detail = fmt.Sprintf("%s, protocol %d.%d", m.Build, proto.Version, m.Level)
	if m.Build == "" {
		detail = fmt.Sprintf("protocol %d.%d", proto.Version, m.Level)
	}
	if m.Build != versionString() || m.Level != proto.Level {
		return false, fmt.Sprintf("%s; this pitwall is %s, protocol %d.%d", detail, versionString(), proto.Version, proto.Level), restart
	}
	return true, detail, ""
}

// hooksRows reports each file `pitwall hooks install` would write: ok when
// a dry run leaves it unchanged. Claude Code's and Codex's files count only
// when that agent is installed.
func hooksRows(home string, short func(string) string, report func(ok bool, what, detail, fix string)) {
	plans, err := planHooks([]string{"install"})
	if err != nil {
		report(false, "hooks", err.Error(), "")
		return
	}
	var skip []string
	if dir := filepath.Join(home, ".claude"); !installed("claude", dir) {
		skip = append(skip, dir+string(os.PathSeparator))
	}
	if dir := agent.CodexDir(home); !installed("codex", dir) {
		skip = append(skip, dir+string(os.PathSeparator))
	}
	for _, p := range plans {
		if slices.ContainsFunc(skip, func(dir string) bool { return strings.HasPrefix(p.path, dir) }) {
			continue
		}
		switch {
		case p.skip != "":
			report(false, "hooks", short(p.path)+" skipped: "+p.skip, "")
		case len(p.changes) == 0:
			report(true, "hooks", short(p.path), "")
		default:
			more := ""
			if len(p.changes) > 1 {
				more = fmt.Sprintf(" and %d more", len(p.changes)-1)
			}
			report(false, "hooks", short(p.path)+" needs: "+p.changes[0]+more, "pitwall hooks install --dry-run, then pitwall hooks install")
		}
	}
}
