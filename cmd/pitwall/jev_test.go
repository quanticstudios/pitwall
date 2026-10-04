package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
)

func TestRunJev(t *testing.T) {
	const key = "ts_live_abcdefghijklmnopqrstuvwxyz012345"
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(decide.KeyEnv, "")
	defer func(p func(context.Context, string, string) (time.Duration, error), r func(io.Reader, io.Writer) (string, error)) {
		jevPing, readSecret = p, r
	}(jevPing, readSecret)
	var pinged string
	pingErr := error(nil)
	jevPing = func(_ context.Context, k, model string) (time.Duration, error) {
		pinged = k
		return 143 * time.Millisecond, pingErr
	}
	readSecret = func(stdin io.Reader, _ io.Writer) (string, error) {
		b, err := io.ReadAll(stdin)
		return string(b), err
	}
	run := func(stdin string, args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := runJev(args, strings.NewReader(stdin), &out, &errb)
		all := out.String() + errb.String()
		if strings.Contains(all, key) {
			t.Fatalf("pitwall jev %v printed the key: %s", args, all)
		}
		return code, all
	}
	cred := filepath.Join(dir, "pitwall", "credentials")

	if code, out := run("", "status"); code != 0 || !strings.Contains(out, "provider:   none") || !strings.Contains(out, "key:        none") {
		t.Fatalf("status before login: %d %q", code, out)
	}
	if code, out := run("not a key\n", "login"); code != 1 || !strings.Contains(out, "spaces") {
		t.Fatalf("bad key: %d %q", code, out)
	}
	code, out := run(key+"\n", "login")
	if code != 0 || !strings.Contains(out, "connection: ok, 143 ms") || pinged != key {
		t.Fatalf("login: %d %q", code, out)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(cred); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("credentials: %v %v", fi, err)
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "pitwall", "config.toml"))
	if strings.Contains(string(cfg), key) || config.LoadDecisions(config.Path()).Provider != "jev" {
		t.Fatalf("config after login: %s", cfg)
	}

	pingErr = errors.New("jev: HTTP 401, the API key was refused")
	if code, out := run("", "status"); code != 1 || !strings.Contains(out, "connection: failed: jev: HTTP 401") {
		t.Fatalf("failing status: %d %q", code, out)
	}
	pingErr = nil
	if code, out := run("", "status"); code != 0 || !strings.Contains(out, "approvals suggest, triage on") {
		t.Fatalf("status: %d %q", code, out)
	}

	if code, out := run("", "logout"); code != 0 || !strings.Contains(out, "Decisions are off") {
		t.Fatalf("logout: %d %q", code, out)
	}
	if _, err := os.Stat(cred); !os.IsNotExist(err) {
		t.Errorf("credentials left behind: %v", err)
	}
	if p := config.LoadDecisions(config.Path()).Provider; p != "" {
		t.Errorf("provider after logout = %q", p)
	}
	if code, _ := run("", "nope"); code != 2 {
		t.Error("unknown subcommand accepted")
	}
}
