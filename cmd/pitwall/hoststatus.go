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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/nowindow"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// daemonMark starts the second line remote-status prints: whether a
// daemon runs on the host, as free text.
const daemonMark = "pitwall-daemon"

// statusTimeout bounds one host's check, ssh's connect included. A var so
// tests wait out a shorter one.
var statusTimeout = 15 * time.Second

// Keep only the tail of each stream, even when a shell startup file is noisy.
const statusOutputLimit = 8 * 1024

type statusOutput struct {
	data      []byte
	truncated bool
	written   bool
	usage     bool // the output began with an older pitwall's usage text
}

func (b *statusOutput) Write(p []byte) (int, error) {
	n := len(p)
	if !b.written && n > 0 {
		b.written, b.usage = true, bytes.HasPrefix(p, []byte("usage:"))
	}
	b.truncated = b.truncated || len(b.data)+n > statusOutputLimit
	if n >= statusOutputLimit {
		b.data = append(b.data[:0], p[n-statusOutputLimit:]...)
	} else {
		if excess := len(b.data) + n - statusOutputLimit; excess > 0 {
			b.data = b.data[excess:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (b *statusOutput) String() string {
	if b.truncated && b.usage {
		// why: an older pitwall's help runs past the limit; its first word is safe to keep.
		return "usage: (help text over 8 KiB omitted)"
	}
	if b.truncated {
		// A retained tail may start inside a secret and lack the marker
		// Redact needs, such as API_TOKEN= or BEGIN PRIVATE KEY.
		return "ssh output exceeded 8 KiB; diagnostics omitted"
	}
	return string(b.data)
}

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
	var stdout, stderr statusOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// why: a shared connection's master stays in the background holding the pipes.
	cmd.WaitDelay = time.Second
	start := time.Now()
	err := cmd.Run()
	st := r.parseStatus(stdout.String(), stderr.String(), err, ctx.Err() != nil)
	st.RTTMillis = time.Since(start).Milliseconds()
	return st
}

// parseStatus judges one remote-status run: its stdout, ssh's stderr, the
// run's error and whether it timed out.
func (r *sshHost) parseStatus(out, stderr string, err error, timedOut bool) hostStatus {
	st := hostStatus{Host: r.name}
	login := hostLoginCommand(r.name)
	var exit *exec.ExitError
	switch {
	case timedOut:
		st.Error, st.Fix = r.timeoutError(stderr, statusTimeout), login
		return st
	case err != nil && errors.As(err, &exit) && exit.ExitCode() == 255:
		st.Error, st.Fix = unreachableError(stderr, err), login
		return st
	case err != nil && errors.As(err, &exit) && exit.ExitCode() == 2 && strings.Contains(stderr, "usage:"):
		st.Reachable = true
		st.Error, st.Fix = fmt.Sprintf("pitwall on %s is too old for pitwall host status", r.name), r.install()
		return st
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		st.Reachable = errors.As(err, &exit)
		st.Error = r.sshError(sshTail(stderr), err).Error()
		return st
	}
	st.Reachable = true
	var header string
	daemonSeen := false
	invalid := func() hostStatus {
		st.Error = fmt.Sprintf("pitwall on %s did not report a valid status", r.name)
		return st
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 5)
		switch {
		case f[0] == remoteMark:
			if len(f) != 5 || header != "" {
				return invalid()
			}
			var protocolErr, levelErr error
			st.Protocol, protocolErr = strconv.Atoi(f[1])
			st.Level, levelErr = strconv.Atoi(f[2])
			if protocolErr != nil || levelErr != nil || st.Protocol < 1 || st.Level < 0 || f[3] == "" || f[4] == "" {
				return invalid()
			}
			st.Version = logs.Redact(f[3])
			// Reuse the existing version-mismatch message, with only this
			// validated record. Never pass shell startup output to parseStart.
			header = strings.Join([]string{remoteMark, f[1], f[2], st.Version, f[4]}, "\t")
		case f[0] == daemonMark:
			if len(f) != 2 || daemonSeen || f[1] == "" {
				return invalid()
			}
			daemonSeen = true
			st.Daemon = logs.Redact(f[1])
		}
	}
	if header == "" || !daemonSeen {
		return invalid()
	}
	if _, err := r.parseStart(header); err != nil {
		st.Error = err.Error() // it ends in the install command
		return st
	}
	if st.Daemon != "not running" && !strings.HasPrefix(st.Daemon, "running ") {
		st.Error = "daemon on " + r.name + ": " + st.Daemon
		return st
	}
	st.OK = true
	return st
}

// timeoutError is a run of ssh killed after timeout, with what it printed.
func (r *sshHost) timeoutError(stderr string, timeout time.Duration) string {
	msg := fmt.Sprintf("ssh to %s timed out after %s", r.name, timeout)
	if tail := sshTail(stderr); tail != "" {
		msg += "; ssh said:\n" + tail
	}
	return msg
}

// unreachableError is a run ssh itself failed, exit status 255.
func unreachableError(stderr string, err error) string {
	return "unreachable: " + cmp.Or(sshTail(stderr), err.Error())
}

// loginError is an ssh failure that waits on a person: a Tailscale check,
// a password or code, or a host key. pitwall host login is where they
// answer it.
type loginError struct {
	reason string // one line
	url    string // the Tailscale check's page, else ""
	detail string // as pitwall host status words it, with what ssh printed
	fix    string
	// open reports whether the shared connection is up, as it is after
	// pitwall host login; nil where ssh shares none.
	open func() bool
}

func (e *loginError) Error() string { return e.detail + "\nfix: " + e.fix }

var (
	checkURL = regexp.MustCompile(`To authenticate, visit:\s*(https://[^\s\x00-\x1f\x7f]+)`)
	// A prompt ssh printed and still waits on ends the output.
	promptLine = regexp.MustCompile(`(?i)(password|passphrase|verification code)[^\n]*:\s*$`)
)

// needsLogin is the loginError for a run of ssh that timed out after
// timeout (0: it exited 255 on its own) having printed stderr, or nil when
// stderr asks nothing of a person.
func (r *sshHost) needsLogin(stderr string, err error, timeout time.Duration) *loginError {
	clean := logs.Redact(stderr)
	e := &loginError{fix: hostLoginCommand(r.name)}
	switch {
	case strings.Contains(clean, "Tailscale SSH requires an additional check") && !strings.Contains(clean, "Authentication checked with Tailscale SSH"):
		e.reason = "Tailscale SSH wants an approval"
		// why: a check URL is new on every try; the last one is current.
		if m := checkURL.FindAllStringSubmatch(clean, -1); m != nil {
			e.url = m[len(m)-1][1]
		}
	case strings.Contains(clean, "Host key verification failed"), strings.Contains(clean, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		e.reason = "ssh does not trust the host key"
	case strings.Contains(clean, "Permission denied ("), strings.Contains(clean, "Too many authentication failures"):
		e.reason = "ssh needs a password, key or code"
	case timeout > 0 && promptLine.MatchString(strings.TrimSpace(clean)):
		e.reason = "ssh is asking for a password or code"
	default:
		return nil
	}
	if timeout > 0 {
		e.detail = r.timeoutError(stderr, timeout)
	} else {
		e.detail = unreachableError(stderr, err)
	}
	e.open = r.sharedCheck()
	return e
}

// sshTail is the last lines ssh printed, where it says why it failed: a
// host key, a refused key, or a Tailscale check's URL.
func sshTail(stderr string) string {
	stderr = logs.Redact(stderr)
	if len(stderr) > statusOutputLimit {
		stderr = stderr[len(stderr)-statusOutputLimit:]
	}
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
		return fmt.Errorf("pitwall host login asks for a password or check at a terminal and this one has none; run %s yourself in a shell", hostLoginCommand(name))
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

func hostLoginCommand(name string) string {
	return "pitwall host login -- " + shellQuote(name)
}
