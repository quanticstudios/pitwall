package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// remoteMark starts the line remote-start prints. pitwall --host reads it
// from the host's pitwall of any version, so its fields never change:
// proto.Version, proto.Level, the version and the socket, tab-separated.
const remoteMark = "pitwall-remote"

const getScript = "https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh"

// parseHost reads pitwall --host's arguments: <host> [-s <session>].
func parseHost(args []string) (host, session string, ok bool) {
	switch {
	case len(args) == 1 && args[0] != "":
		return args[0], "", true
	case len(args) == 3 && args[0] != "" && args[1] == "-s" && args[2] != "":
		return args[0], args[2], true
	}
	return "", "", false
}

// sshHost is a daemon on an ssh host. One ssh connection, shared through
// ControlMaster, starts it and forwards its socket to a socket here, which
// the window dials. Everything else about the connection (user, port, key,
// jump host) is the user's ssh config's.
type sshHost struct {
	name  string // as given to --host
	ssh   string // the ssh binary
	dest  string // the ssh destination
	dir   string // here: the shared connection's control socket, and local
	local string // the forwarded socket here

	// batch keeps ssh from asking for a password or passphrase: an agent's
	// pane has no one to answer, and a prompt would hang the command.
	batch bool

	mu   sync.Mutex
	sock string // the daemon's socket on the host, once known
}

// newSSHHost finds name's ssh destination in hosts, else uses name as one.
func newSSHHost(name string, hosts []config.Host) (*sshHost, error) {
	dest := name
	for _, h := range hosts {
		if h.Name == name && h.SSH != "" {
			dest = h.SSH
		}
	}
	if strings.HasPrefix(dest, "-") {
		return nil, fmt.Errorf("%q is not an ssh destination", dest)
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("pitwall --host needs ssh: %w", err)
	}
	path, err := proto.SocketPath() // makes the private directory
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	// One window can forward several hosts' sockets.
	local := filepath.Join(dir, fmt.Sprintf("remote-%d-%d.sock", os.Getpid(), forwards.Add(1)))
	return &sshHost{name: name, ssh: ssh, dest: dest, dir: dir, local: local}, nil
}

// forwards counts the sshHosts this process made.
var forwards atomic.Int32

// sshArgs are ssh's arguments: the shared connection's options, opts, the
// destination, then command. The first call opens the connection, the
// others reuse it, and it closes 10 minutes after its last use.
func (r *sshHost) sshArgs(opts []string, command ...string) []string {
	args := append(append(r.shared(), opts...), "--", r.dest)
	return append(args, command...)
}

// shared are the options of the shared connection.
func (r *sshHost) shared() []string {
	args := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(r.dir, "ssh-%C"),
		"-o", "ControlPersist=10m",
		// why: a forward from a connection that dropped leaves its socket file.
		"-o", "StreamLocalBindUnlink=yes",
		// why: a link that died silently, as when the laptop slept, ends the
		// connection within a minute, and the window reconnects.
		"-o", "ServerAliveInterval=15",
		"-o", "ConnectTimeout=10",
	}
	if r.batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	return args
}

// gitSSH is GIT_SSH_COMMAND for a git that reaches the host over the
// shared connection.
func (r *sshHost) gitSSH() string {
	words := []string{shellQuote(r.ssh)}
	for _, a := range r.shared() {
		words = append(words, shellQuote(a))
	}
	return strings.Join(words, " ")
}

// shellQuote is s in single quotes for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// startCommand runs remote-start on the host. ssh hands it to the host's
// login shell; every common one reads the single quotes alike, and sh then
// looks in ~/.local/bin too, where get.sh installs and which the PATH of a
// non-interactive ssh login often lacks.
func startCommand(restart bool) string {
	cmd := "pitwall remote-start"
	if restart {
		cmd += " --restart"
	}
	return `sh -c 'PATH="$PATH:$HOME/.local/bin" exec ` + cmd + `'`
}

// socket makes sure a daemon runs on the host, forwards its socket here
// and connects to it.
func (r *sshHost) socket(restart bool) (net.Conn, error) {
	sock, err := r.start(restart)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.sock = sock
	r.mu.Unlock()
	if err := r.control("forward", sock); err != nil {
		return nil, fmt.Errorf("forward pitwall's socket from %s: %w", r.name, err)
	}
	return net.Dial("unix", r.local)
}

// dialHost connects to the daemon on [[hosts]] entry name as a client of
// kind ("cli" or "watch", see proto.Hello); done drops the forward. From a
// pitwall pane ssh never prompts. Tests replace it.
var dialHost = func(name, kind string) (conn *cliConn, done func(), err error) {
	if runtime.GOOS == "windows" {
		return nil, nil, errors.New("--host needs ssh's connection sharing (ControlMaster), which Windows' ssh lacks")
	}
	s, _ := config.Load()
	if err := agentHost(name, s.Hosts); err != nil {
		return nil, nil, err
	}
	r, err := newSSHHost(name, s.Hosts)
	if err != nil {
		return nil, nil, err
	}
	r.batch = os.Getenv("PITWALL_PANE") != ""
	nc, err := r.socket(false)
	if err != nil {
		r.close()
		return nil, nil, err
	}
	conn, err = hello(nc, kind)
	if err != nil {
		r.close()
		return nil, nil, err
	}
	return conn, func() { conn.Close(); r.close() }, nil
}

// agentHost refuses, in a pitwall pane, a host that is not a [[hosts]]
// name: an agent starts processes only on machines the user listed. A
// person at a shell may still give any ssh destination.
func agentHost(name string, hosts []config.Host) error {
	if os.Getenv("PITWALL_PANE") == "" || slices.ContainsFunc(hosts, func(h config.Host) bool { return h.Name == name }) {
		return nil
	}
	return fmt.Errorf("in a pitwall pane, --host takes only a [[hosts]] name; add [[hosts]] name = %q to %s", name, config.Path())
}

// start runs remote-start on the host and returns the daemon's socket
// there, or why this pitwall cannot use it.
func (r *sshHost) start(restart bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.ssh, r.sshArgs(nil, startCommand(restart))...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// why: the shared connection's process stays in the background; ssh
	// detaches it from the pipes, but a wait must never depend on that.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("ssh to %s timed out", r.name)
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		return "", r.sshError(stderr.String(), err)
	}
	return r.parseStart(string(out))
}

// parseStart reads remote-start's line, skipping whatever the host's shell
// startup files print.
func (r *sshHost) parseStart(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 5)
		if len(f) < 5 || f[0] != remoteMark {
			continue
		}
		v, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		if v != proto.Version {
			hint := "Install a matching pitwall there with:"
			if v > proto.Version {
				hint = "Update this pitwall, or install a matching one there with:"
			}
			return "", fmt.Errorf("pitwall %s on %s speaks protocol version %d and this pitwall %s speaks %d. %s\n  %s",
				f[3], r.name, v, versionString(), proto.Version, hint, r.install())
		}
		return f[4], nil
	}
	return "", fmt.Errorf("pitwall on %s did not report its socket; it printed %q", r.name, out)
}

// sshError explains a failed remote-start from ssh's exit status: 127 is
// the shell's "not found", 255 ssh's own failure, and 2 with the usage a
// pitwall from before remote-start.
func (r *sshHost) sshError(stderr string, err error) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fmt.Errorf("run ssh: %w", err)
	}
	msg := strings.TrimPrefix(lastLine(stderr), "pitwall: ")
	if msg == "" {
		msg = err.Error()
	}
	switch code := exit.ExitCode(); {
	case code == 127:
		return fmt.Errorf("pitwall is not installed on %s: it is not on the PATH there or in ~/.local/bin. Install it with:\n  %s", r.name, r.install())
	case code == 2 && strings.Contains(stderr, "usage:"):
		return fmt.Errorf("pitwall on %s is too old to serve a window over ssh. Update it with:\n  %s", r.name, r.install())
	case code == 255:
		return fmt.Errorf("cannot reach %s over ssh: %s", r.name, msg)
	}
	return fmt.Errorf("pitwall on %s failed: %s", r.name, msg)
}

// install is the command that installs pitwall on the host: this release
// when this pitwall is one, else the latest.
func (r *sshHost) install() string {
	pin := ""
	if strings.HasPrefix(version, "v") {
		pin = "PITWALL_VERSION=" + version + " "
	}
	return fmt.Sprintf("ssh %s 'curl -fsSL %s | %ssh'", r.dest, getScript, pin)
}

// control asks the shared connection to forward or cancel the forward of
// sock on the host to r.local.
func (r *sshHost) control(op, sock string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, r.ssh, r.sshArgs([]string{"-O", op, "-L", r.local + ":" + sock})...).CombinedOutput()
	if err != nil {
		if msg := lastLine(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// close drops the forward and its socket here. The host's daemon keeps
// running, and the shared connection ends when no window uses it.
func (r *sshHost) close() {
	r.mu.Lock()
	sock := r.sock
	r.mu.Unlock()
	if sock != "" {
		r.control("cancel", sock)
	}
	os.Remove(r.local)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// runRemoteStart is what pitwall --host runs on the host: it starts a
// daemon unless one runs and prints its socket after remoteMark. With
// --restart it first stops a daemon that refuses this proto.Version.
func runRemoteStart(args []string, out io.Writer) error {
	path, err := socketPath()
	if err != nil {
		return err
	}
	nc, err := net.Dial("unix", path)
	if err == nil && slices.Equal(args, []string{"--restart"}) && refuses(nc) {
		nc.Close()
		if err := stopIncompatibleDaemon(path); err != nil {
			return err
		}
		err = errors.New("stopped")
	}
	if err != nil {
		if nc, err = startDaemon(path); err != nil {
			return err
		}
	}
	nc.Close()
	_, err = fmt.Fprintf(out, "%s\t%d\t%d\t%s\t%s\n", remoteMark, proto.Version, proto.Level, versionString(), path)
	return err
}

// refuses reports whether the daemon on nc turns this proto.Version away.
func refuses(nc net.Conn) bool {
	conn := proto.NewConn(nc)
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Send(proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "cli"})
	conn.Send(proto.Sync{})
	for {
		msg, err := conn.Recv()
		if err != nil {
			return false
		}
		switch m := msg.(type) {
		case proto.StateMsg:
			return false
		case proto.Error:
			_, refused := proto.RefusedVersion(m)
			return refused
		}
	}
}

// partOn is session's part in hs, a host's state: the part session links,
// else the one made for origin, else, with byName (a person's action), a
// session of the same name made for no other session. A part made for
// another session never is: a kill of this one would close its tabs. A
// returned part whose Origin is not origin still needs a SetOrigin.
func partOn(hs model.State, session *model.Session, host string, origin model.Origin, byName bool) *model.Session {
	ours := func(p *model.Session) bool {
		return p != nil && (p.Origin == origin || p.Origin == model.Origin{})
	}
	if r := session.Remote(host); r != nil && ours(hs.Session(r.Session)) {
		return hs.Session(r.Session)
	}
	if p := hs.PartOf(origin); p != nil {
		return p
	}
	if p := hs.SessionNamed(session.Name); byName && ours(p) {
		return p
	}
	return nil
}

// lostAfter is how long a recorded worker may be missing from its host's
// current state before it counts as never started.
const lostAfter = 2 * time.Minute

// workerFixes compares session's records of its workers on host with hs,
// that host's current state, in which origin made session's part. A
// worker whose parent there is not the record's, as after its parent here
// reopened with a new id, gets a Reparent. A record hs has neither live
// nor closed lostAfter after Since comes back marked lost.
func workerFixes(session model.Session, host string, hs model.State, origin model.Origin, now time.Time) (moves []proto.Reparent, lost []model.RemoteWorker) {
	for _, r := range session.Workers {
		if r.Host != host || r.Lost != "" {
			continue
		}
		if w := hs.Workspace(r.ID); w != nil {
			if w.Parent != r.Parent {
				moves = append(moves, proto.Reparent{WorkspaceID: r.ID, ParentTab: r.Parent, Origin: origin})
			}
			continue
		}
		if i := slices.IndexFunc(hs.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == r.ID }); i >= 0 {
			if hs.Closed[i].Tab.Parent != r.Parent {
				moves = append(moves, proto.Reparent{WorkspaceID: r.ID, ParentTab: r.Parent, Origin: origin})
			}
			continue
		}
		if now.Sub(r.Since) > lostAfter {
			r.Lost = "never started"
			lost = append(lost, r)
		}
	}
	return moves, lost
}
