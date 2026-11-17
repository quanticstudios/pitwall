//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func isolatedStatusHost(t *testing.T, configText string) string {
	t.Helper()
	path := os.Getenv("PATH")
	hooksHome(t)
	t.Setenv("PATH", path)
	dir := fakeHost(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PITWALL_SOCKET", sockPath(t, "none.sock"))
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("SSH_CONNECTION", "")
	if err := os.MkdirAll(filepath.Dir(configPath(t)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(t), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHostLoginAndStatus(t *testing.T) {
	dir := isolatedStatusHost(t, "[[hosts]]\nname = \"box\"\nssh = \"me@box\"\n")
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	var out, errb bytes.Buffer
	if code := runHost([]string{"login", "box"}, null, &out, &errb); code != 0 || !strings.Contains(out.String(), "ok   box:") {
		t.Fatalf("login exit %d: %s%s", code, &out, &errb)
	}
	logPath := filepath.Join(dir, "ssh.log")
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "BatchMode=yes") || !strings.HasSuffix(lines[0], " -- me@box true") ||
		!strings.Contains(lines[1], "BatchMode=yes") || !strings.Contains(lines[1], "remote-status") {
		t.Fatalf("login must allow prompts then check in batch mode: %q", lines)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "down"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := runHost([]string{"login", "box"}, null, &out, &errb); code != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "ssh to box:") {
		t.Fatalf("failed login exit %d: %s%s", code, &out, &errb)
	}
	log, err = os.ReadFile(logPath)
	if err != nil || strings.Contains(string(log), "remote-status") {
		t.Fatalf("failed login ran a status check: %s, %v", log, err)
	}
}

func TestHostStatusTruncatedDiagnostics(t *testing.T) {
	dir := isolatedStatusHost(t, "")
	for _, diagnostic := range []string{
		"API_TOKEN=" + strings.Repeat("example-secret", statusOutputLimit/4),
		"-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("example-secret\n", statusOutputLimit/4) + "-----END PRIVATE KEY-----\n" + strings.Repeat("x", 7000),
	} {
		script := "#!/bin/sh\ncat <<'DIAGNOSTICS' >&2\n" + diagnostic + "\nDIAGNOSTICS\nexit 255\n"
		ssh := filepath.Join(dir, "bin", "ssh")
		if err := os.WriteFile(ssh, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		st := checkHost(&sshHost{name: "box", dest: "box", ssh: ssh, dir: dir})
		if st.OK || strings.Contains(st.Error, "example-secret") || !strings.Contains(st.Error, "diagnostics omitted") {
			t.Fatalf("truncated diagnostics leaked: %+v", st)
		}
	}
}

// Two stuck hosts share one timeout budget. The fake ssh replaces itself
// with sleep, so CommandContext kills the only process it starts.
func TestDoctorHostTimeoutsConcurrent(t *testing.T) {
	dir := isolatedStatusHost(t, "[[hosts]]\nname = \"box\"\n[[hosts]]\nname = \"box2\"\n[[hosts]]\nname = \"ready\"\n")
	script := fmt.Sprintf(`#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
if [ "$1" = ready ]; then
  printf '%s\t%d\t%d\t%s\t/socket\n%s\tnot running\n'
  exit 0
fi
printf 'API_TOKEN=example-secret\n# To authenticate, visit: https://login.example.com/a/1\n' >&2
exec sleep 60
`, remoteMark, proto.Version, proto.Level, versionString(), daemonMark)
	if err := os.WriteFile(filepath.Join(dir, "bin", "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	start := time.Now()
	code := runDoctor(&out)
	elapsed := time.Since(start)
	if elapsed > statusTimeout+5*time.Second || elapsed < statusTimeout {
		t.Fatalf("doctor took %s, want one %s timeout", elapsed, statusTimeout)
	}
	for _, want := range []string{"FAIL host box:", "FAIL host box2:", "ok   host ready:", "timed out after 15s", "https://login.example.com/a/1"} {
		if code != 1 || !strings.Contains(out.String(), want) {
			t.Errorf("doctor exit %d missing %q: %s", code, want, &out)
		}
	}
	if strings.Contains(out.String(), "example-secret") {
		t.Fatalf("doctor leaked a secret: %s", &out)
	}
}
