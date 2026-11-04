package teardown

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestRun(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	s := config.WorktreeSettings{Teardown: `printf '%s %s %s' "$PORT" "$PITWALL_PORT_BASE" "$PITWALL_PORTS" > ports; echo stopped`}
	ports := model.PortBlock{First: 3010, Last: 3019}
	notice, err := Run(context.Background(), dir, s, ports, "")
	data, readErr := os.ReadFile(filepath.Join(dir, "ports"))
	if err != nil || readErr != nil || string(data) != "3010 3010 3010-3019" || !strings.Contains(notice, "stopped") {
		t.Fatalf("%q %v, ports %q %v", notice, err, data, readErr)
	}
	log, err := os.ReadFile(filepath.Join(config.StateDir(), "teardown.log"))
	if err != nil || !strings.Contains(string(log), "stopped") || !strings.Contains(notice, "teardown.log") {
		t.Fatalf("output log: %q %v", log, err)
	}
	s.Teardown = "echo failure-output; exit 7"
	notice, err = Run(context.Background(), dir, s, ports, "")
	if err == nil || !strings.Contains(err.Error(), "worktree kept") || !strings.Contains(notice, "failure-output") {
		t.Fatalf("%q %v", notice, err)
	}
	log, readErr = os.ReadFile(filepath.Join(config.StateDir(), "teardown.log"))
	if readErr != nil || !strings.Contains(string(log), "Teardown failed: exit status 7") {
		t.Fatalf("failure reason missing from log: %q %v", log, readErr)
	}
	s.Teardown = "touch approved"
	s.TeardownFromRepo = true
	for _, approval := range []string{"", "touch another"} {
		notice, err = Run(context.Background(), dir, s, ports, approval)
		if err != nil || !strings.Contains(notice, "Teardown skipped") || !strings.Contains(notice, s.Teardown) {
			t.Fatalf("%q %v", notice, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "approved")); !os.IsNotExist(err) {
			t.Fatal("unapproved repo command ran")
		}
	}
	if _, err := Run(context.Background(), dir, s, ports, s.Teardown); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "approved")); err != nil {
		t.Fatal(err)
	}
}

func TestTimeoutKillsGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	s := config.WorktreeSettings{Teardown: `sh -c 'echo child-ready; sleep 0.8; touch survived' & wait`}
	notice, err := Run(ctx, dir, s, model.PortBlock{}, "")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(notice, "child-ready") {
		t.Fatalf("%q %v", notice, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout did not stop the command")
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(filepath.Join(dir, "survived")); !os.IsNotExist(err) {
		t.Fatal("a child survived the group kill")
	}
	if Timeout != 2*time.Minute {
		t.Fatalf("timeout %v", Timeout)
	}
}

func TestTail(t *testing.T) {
	out := &tail{}
	out.Write([]byte(strings.Repeat("a", 9000)))
	out.Write([]byte("last"))
	if len(out.data) != 8192 || !strings.HasSuffix(string(out.data), "last") {
		t.Fatalf("tail len %d", len(out.data))
	}
}

func TestWaitDelaySucceeds(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	notice, err := Run(ctx, t.TempDir(), config.WorktreeSettings{Teardown: "sleep 5 & echo helper-open"}, model.PortBlock{}, "")
	if err != nil || !strings.Contains(notice, "helper-open") {
		t.Fatalf("background output holder: %q %v", notice, err)
	}
}

func TestEmptyPortsDoNotInherit(t *testing.T) {
	for _, key := range []string{"PORT", "PITWALL_PORT_BASE", "PITWALL_PORTS"} {
		t.Setenv(key, "other-stack")
	}
	notice, err := Run(context.Background(), t.TempDir(), config.WorktreeSettings{Teardown: `printf 'ports=<%s,%s,%s>' "$PORT" "$PITWALL_PORT_BASE" "$PITWALL_PORTS"`}, model.PortBlock{}, "")
	if err != nil || !strings.Contains(notice, "ports=<,,>") {
		t.Fatalf("inherited ports: %q %v", notice, err)
	}
}

func TestMissingFolderSkips(t *testing.T) {
	notice, err := Run(context.Background(), filepath.Join(t.TempDir(), "gone"), config.WorktreeSettings{Teardown: "true"}, model.PortBlock{}, "")
	if err != nil || !strings.Contains(notice, "folder is gone") || !strings.Contains(notice, "true") {
		t.Fatalf("%q %v", notice, err)
	}
}

func TestTimeoutNamesLimit(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errTimeout)
	_, err := Run(ctx, t.TempDir(), config.WorktreeSettings{Teardown: "true"}, model.PortBlock{}, "")
	if err == nil || !strings.Contains(err.Error(), "2 minute teardown limit reached") {
		t.Fatalf("timeout reason: %v", err)
	}
}
