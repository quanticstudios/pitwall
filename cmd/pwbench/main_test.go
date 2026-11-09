package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/drawbench"
)

func TestIsolation(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "/real/socket")
	t.Setenv("PITWALL_PANE", "real")
	t.Setenv("PITWALL_ATTACH", "1")
	t.Setenv("XDG_CONFIG_HOME", "/real/config")
	t.Setenv("HOME", "/real/home")
	t.Setenv("vblank_mode", "1")
	t.Setenv("__GL_SYNC_TO_VBLANK", "1")
	got := isolatedEnv("/private")
	for _, s := range got {
		if strings.HasPrefix(s, "PITWALL_") || strings.Contains(s, "/real/") || strings.HasPrefix(s, "vblank_mode=") || strings.HasPrefix(s, "__GL_SYNC_TO_VBLANK=") {
			t.Fatal(s)
		}
	}
	for key, dir := range map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "config",
		"XDG_STATE_HOME":  "state",
		"XDG_RUNTIME_DIR": "runtime",
	} {
		want := key + "=" + filepath.Join("/private", dir)
		found := false
		for _, s := range got {
			found = found || s == want
		}
		if !found {
			t.Fatal("missing", want)
		}
	}
}

func TestCompareCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	data, _ := json.Marshal(drawbench.Result{Workload: "scroll", Seconds: 1, Build: "test", Frames: drawbench.Frames{Drawn: 3, Scale: 1}})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"compare", path, path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "daemon_cpu_seconds") || !strings.Contains(out.String(), "+0.000") {
		t.Fatal(&out)
	}
	for _, args := range [][]string{{"compare"}, {"-seconds", "NaN"}, {"-workload", "unknown"}, {"fixture", "unknown"}} {
		if err := run(args, &out); err == nil {
			t.Fatal(args)
		}
	}
}

func TestWaitFor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitFor(ctx, time.Second, func() (bool, error) { return false, nil }); err != context.Canceled {
		t.Fatal(err)
	}
	if err := waitFor(context.Background(), time.Millisecond, func() (bool, error) { return false, nil }); err == nil {
		t.Fatal("missing timeout")
	}
}
