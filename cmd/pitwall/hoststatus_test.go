//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// pitwall host status runs remote-status on each [[hosts]] entry through
// ssh that never prompts, and says why a host failed in ssh's own words.
func TestHostStatus(t *testing.T) {
	dir := isolatedStatusHost(t, "[[hosts]]\nname = \"box\"\nssh = \"me@box\"\n")
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := runHost(args, os.Stdin, &out, &errb)
		return code, out.String() + errb.String()
	}
	code, out := run("status")
	want := fmt.Sprintf("ok   box: pitwall %s, protocol %d.%d, daemon not running, ", versionString(), proto.Version, proto.Level)
	if code != 0 || !strings.HasPrefix(out, want) {
		t.Errorf("exit %d, want 0 and %q:\n%s", code, want, out)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "ssh.log"))
	if !strings.Contains(string(log), "BatchMode=yes") || !strings.Contains(string(log), "remote-status") {
		t.Errorf("ssh ran:\n%s", log)
	}

	if err := os.WriteFile(filepath.Join(dir, "down"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = run("status", "--json", "box")
	var sts []hostStatus
	if err := json.Unmarshal([]byte(out), &sts); err != nil || code != 1 || len(sts) != 1 || sts[0].OK || sts[0].Reachable ||
		sts[0].Error != "unreachable: ssh: connect to host box port 22: Connection timed out" || sts[0].Fix != "pitwall host login -- 'box'" {
		t.Errorf("down: exit %d, %v:\n%s", code, err, out)
	}
	var doc bytes.Buffer
	if runDoctor(&doc) != 1 || !strings.Contains(doc.String(), "FAIL host box: unreachable: ssh: connect to host box port 22: Connection timed out\n     fix: pitwall host login -- 'box'\n") {
		t.Errorf("doctor:\n%s", doc.String())
	}

	// From a pane, a name not in [[hosts]] is refused, and login needs a terminal.
	t.Setenv("PITWALL_PANE", "p1")
	if code, out := run("status", "other"); code != 1 || !strings.Contains(out, "takes only a [[hosts]] name") {
		t.Errorf("unlisted host from a pane: exit %d:\n%s", code, out)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	var out2, errb bytes.Buffer
	if code := runHost([]string{"login", "box"}, null, &out2, &errb); code != 1 || !strings.Contains(errb.String(), "run pitwall host login -- 'box' yourself in a shell") {
		t.Errorf("login without a terminal from a pane: exit %d:\n%s", code, errb.String())
	}
}

func configPath(t *testing.T) string {
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pitwall", "config.toml")
}

func TestParseStatus(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })
	version = "v0.2.15"
	r := &sshHost{name: "box", dest: "me@box"}
	exit := func(code int) error { return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run() }
	tailscale := "# Tailscale SSH requires an additional check.\n# To authenticate, visit: https://login.example.com/a/1\n"
	for _, c := range []struct {
		name, out, stderr string
		err               error
		timedOut          bool
		ok, reachable     bool
		want              string
	}{
		{"timeout", "", tailscale, context.DeadlineExceeded, true, false, false,
			"ssh to box timed out after 15s; ssh said:\n# Tailscale SSH requires an additional check.\n# To authenticate, visit: https://login.example.com/a/1"},
		{"host key", "", "line1\nline2\nHost key verification failed.\n", exit(255), false, false, false, "unreachable: line1\nline2\nHost key verification failed."},
		{"old", "", "usage:\n  pitwall ...\n", exit(2), false, false, true, "pitwall on box is too old for pitwall host status"},
		{"missing", "", "sh: pitwall: not found\n", exit(127), false, false, true, "pitwall is not installed on box"},
		{"remote error secret", "", "API_TOKEN=example-secret\n", exit(1), false, false, true, "API_TOKEN=[redacted]"},
		{"other version", fmt.Sprintf("%s\t%d\t3\tv0.0.9\t/s\n%s\tnot running\n", remoteMark, proto.Version+1, daemonMark), "", nil, false, false, true, "Update this pitwall"},
		{"ok", fmt.Sprintf("motd\n%s\t%d\t3\tv0.2.15\t/s\n%s\trunning v0.2.15, protocol %d.3\n", remoteMark, proto.Version, daemonMark, proto.Version), "", nil, false, true, true, ""},
	} {
		st := r.parseStatus(c.out, c.stderr, c.err, c.timedOut)
		if st.OK != c.ok || st.Reachable != c.reachable || !strings.Contains(st.Error, c.want) || (st.OK && st.Daemon == "") {
			t.Errorf("%s: %+v", c.name, st)
		}
	}
}

// An older pitwall's help runs past the output limit; host status still
// says the host is too old instead of only that the output was too long.
func TestStatusOutputKeepsLongUsage(t *testing.T) {
	var b statusOutput
	b.Write([]byte("usage:\n" + strings.Repeat("  pitwall something\n", 1000)))
	if got := b.String(); !strings.HasPrefix(got, "usage:") {
		t.Fatalf("long usage lost its marker: %q", got)
	}
	var noise statusOutput
	noise.Write([]byte("motd\n"))
	noise.Write([]byte("usage:\n" + strings.Repeat("x", statusOutputLimit)))
	if got := noise.String(); strings.HasPrefix(got, "usage:") {
		t.Fatalf("usage after other output kept as safe: %q", got)
	}
}
