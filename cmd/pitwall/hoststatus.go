package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/nowindow"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// daemonMark starts the second line remote-status prints: whether a
// daemon runs on the host, as free text.
const daemonMark = "pitwall-daemon"

// statusTimeout bounds one host's check, ssh's connect included.
const statusTimeout = 15 * time.Second

const hostUsage = `usage:
  pitwall host status [--json] [name]
                             check each [[hosts]] entry, or name, over ssh without prompting
  pitwall host login <name>  ssh to name at this terminal to answer a password, MFA or
                             Tailscale check, then check it
`

// runRemoteStatus is what pitwall host status runs on the host: remote-start's
// line, then whether a daemon runs. It never starts one.
func runRemoteStatus(out io.Writer) error {
	path, err := socketPath()
	if err != nil {
		return err
	}
	daemon := "not running"
	if m, err := doctorDaemon(); err == nil {
		daemon = fmt.Sprintf("running %s, protocol %d.%d", m.Build, proto.Version, m.Level)
	} else if !errors.Is(err, errNotRunning) {
		daemon = strings.ReplaceAll(err.Error(), "\n", " ")
	}
	_, err = fmt.Fprintf(out, "%s\t%d\t%d\t%s\t%s\n%s\t%s\n", remoteMark, proto.Version, proto.Level, versionString(), path, daemonMark, daemon)
	return err
}

// hostStatus is one host's line in pitwall host status and doctor.
type hostStatus struct {
	Host      string `json:"host"`
	OK        bool   `json:"ok"`
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`  // the host's pitwall
	Protocol  int    `json:"protocol,omitempty"` // its proto.Version
	Level     int    `json:"level,omitempty"`
	Daemon    string `json:"daemon,omitempty"` // "not running", "running <build>, protocol V.L", or why it did not answer
	RTTMillis int64  `json:"rtt_ms"`           // the whole check, ssh's connect included unless a shared connection was open
	Error     string `json:"error,omitempty"`
	Fix       string `json:"fix,omitempty"`
}

// detail is st after "host <name>: " in a status or doctor line.
func (st hostStatus) detail() string {
	if st.Error != "" {
		return strings.ReplaceAll(st.Error, "\n", "\n     ")
	}
	d := fmt.Sprintf("pitwall %s, protocol %d.%d", st.Version, st.Protocol, st.Level)
	if st.Level != proto.Level {
		d += fmt.Sprintf(" (this pitwall %d.%d)", proto.Version, proto.Level)
	}
	return fmt.Sprintf("%s, daemon %s, %dms", d, st.Daemon, st.RTTMillis)
}

// checkHost runs remote-status on r's host with ssh that never prompts.
func checkHost(r *sshHost) hostStatus {
	r.batch = true
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.ssh, r.sshArgs(nil, strings.Replace(startCommand(false), "remote-start", "remote-status", 1))...)
	nowindow.Set(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// why: a shared connection's master stays in the background holding the pipes.
	cmd.WaitDelay = time.Second
	start := time.Now()
	out, err := cmd.Output()
	st := r.parseStatus(string(out), stderr.String(), err, ctx.Err() != nil)
	st.RTTMillis = time.Since(start).Milliseconds()
	return st
}

// parseStatus judges one remote-status run: its stdout, ssh's stderr, the
// run's error and whether it timed out.
func (r *sshHost) parseStatus(out, stderr string, err error, timedOut bool) hostStatus {
	st := hostStatus{Host: r.name}
	login := "pitwall host login " + r.name
	var exit *exec.ExitError
	switch {
	case timedOut:
		st.Error, st.Fix = fmt.Sprintf("ssh to %s timed out after %s", r.name, statusTimeout), login
		if tail := sshTail(stderr); tail != "" {
			st.Error += "; ssh said:\n" + tail
		}
		return st
	case err != nil && errors.As(err, &exit) && exit.ExitCode() == 255:
		st.Error, st.Fix = "unreachable: "+cmp.Or(sshTail(stderr), err.Error()), login
		return st
	case err != nil && errors.As(err, &exit) && exit.ExitCode() == 2 && strings.Contains(stderr, "usage:"):
		st.Reachable = true
		st.Error, st.Fix = fmt.Sprintf("pitwall on %s is too old for pitwall host status", r.name), r.install()
		return st
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		st.Reachable = errors.As(err, &exit)
		st.Error = r.sshError(stderr, err).Error()
		return st
	}
	st.Reachable = true
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 5)
		switch {
		case len(f) == 5 && f[0] == remoteMark:
			st.Protocol, _ = strconv.Atoi(f[1])
			st.Level, _ = strconv.Atoi(f[2])
			st.Version = f[3]
		case len(f) == 2 && f[0] == daemonMark:
			st.Daemon = f[1]
		}
	}
	if _, err := r.parseStart(out); err != nil {
		st.Error = err.Error() // it ends in the install command
		return st
	}
	st.OK = true
	return st
}

// sshTail is the last lines ssh printed, where it says why it failed: a
// host key, a refused key, or a Tailscale check's URL.
func sshTail(stderr string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-3):], "\n")
}

// checkHosts checks names, or every [[hosts]] entry when there are none,
// at once, in order.
func checkHosts(names []string, hosts []config.Host) []hostStatus {
	if len(names) == 0 {
		for _, h := range hosts {
			names = append(names, h.Name)
		}
	}
	out := make([]hostStatus, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			r, err := newSSHHost(name, hosts)
			if err != nil {
				out[i] = hostStatus{Host: name, Error: err.Error()}
				return
			}
			out[i] = checkHost(r)
		})
	}
	wg.Wait()
	return out
}

// runHost is pitwall host.
func runHost(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, hostUsage)
		return 2
	}
	flags := flag.NewFlagSet("host "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() > 1 || (args[0] == "login" && (flags.NArg() != 1 || *asJSON)) {
		fmt.Fprint(stderr, hostUsage)
		return 2
	}
	s, _ := config.Load()
	names := flags.Args()
	for _, name := range names {
		if err := agentHost(name, s.Hosts); err != nil {
			fmt.Fprintln(stderr, "pitwall:", err)
			return 1
		}
	}
	switch args[0] {
	case "status":
		if len(names) == 0 && len(s.Hosts) == 0 && !*asJSON {
			fmt.Fprintf(stdout, "no [[hosts]] in %s\n", config.Path())
			return 0
		}
		return printHosts(checkHosts(names, s.Hosts), *asJSON, stdout)
	case "login":
		if err := hostLogin(names[0], s.Hosts, stdin); err != nil {
			fmt.Fprintln(stderr, "pitwall:", err)
			return 1
		}
		return printHosts(checkHosts(names, s.Hosts), false, stdout)
	}
	fmt.Fprint(stderr, hostUsage)
	return 2
}

// printHosts prints sts as doctor lines or JSON, and is 0 when all are ok.
func printHosts(sts []hostStatus, asJSON bool, w io.Writer) int {
	code := 0
	for _, st := range sts {
		if !st.OK {
			code = 1
		}
	}
	if asJSON {
		if sts == nil {
			sts = []hostStatus{}
		}
		json.NewEncoder(w).Encode(sts)
		return code
	}
	for _, st := range sts {
		mark := "ok  "
		if !st.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "%s %s: %s\n", mark, st.Host, st.detail())
		if st.Fix != "" {
			fmt.Fprintf(w, "     fix: %s\n", st.Fix)
		}
	}
	return code
}

// hostLogin runs ssh to name at this terminal, prompts allowed, through the
// shared connection the next --host reuses. An agent in a pane has no one
// at its stdin to answer, so it may not.
func hostLogin(name string, hosts []config.Host, stdin *os.File) error {
	if os.Getenv("PITWALL_PANE") != "" && !term.IsTerminal(stdin.Fd()) {
		return fmt.Errorf("pitwall host login asks for a password or check at a terminal and this one has none; run pitwall host login %s yourself in a shell", name)
	}
	r, err := newSSHHost(name, hosts)
	if err != nil {
		return err
	}
	cmd := exec.Command(r.ssh, r.sshArgs(nil, "true")...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh to %s: %w", name, err)
	}
	return nil
}
