package update

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMain idles instead of testing when TestSwapRunning runs a copy of
// this binary.
func TestMain(m *testing.M) {
	if os.Getenv("PITWALL_UPDATE_IDLE") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestSwapRunning swaps a binary that is running, twice, as two updates
// before the daemon restarts would, then removes what it set aside once the
// process is gone.
func TestSwapRunning(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pitwall.exe")
	copyFile(t, os.Args[0], exe)
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "PITWALL_UPDATE_IDLE=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if err := os.Remove(exe); err == nil {
		t.Fatal("removed a running binary; this test proves nothing here")
	}
	for _, v := range []string{"v2", "v3"} {
		next := filepath.Join(dir, "next")
		if err := os.WriteFile(next, []byte(v), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := swap(exe, next); err != nil {
			t.Fatalf("swap to %s: %v", v, err)
		}
		if got, _ := os.ReadFile(exe); string(got) != v {
			t.Fatalf("after the swap to %s: %q", v, got)
		}
	}
	RemoveOld(exe)
	if _, err := os.Stat(exe + ".old"); err != nil {
		t.Fatalf("removed the binary that still runs: %v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	RemoveOld(exe)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
