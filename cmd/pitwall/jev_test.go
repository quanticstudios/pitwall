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
	"github.com/quanticstudios/pitwall/internal/decisionlog"
)

func TestRunJev(t *testing.T) {
	const key = "ts_live_abcdefghijklmnopqrstuvwxyz012345"
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
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
	// Connecting keeps the user's feature settings and says what is on.
	os.MkdirAll(filepath.Join(dir, "pitwall"), 0o700)
	os.WriteFile(filepath.Join(dir, "pitwall", "config.toml"), []byte("[decisions.triage]\nenabled = false\n"), 0o644)
	code, out := run(key+"\n", "login")
	if code != 0 || !strings.Contains(out, "connection: ok, 143 ms") || pinged != key || !strings.Contains(out, "approvals suggest, triage off") {
		t.Fatalf("login: %d %q", code, out)
	}
	if strings.Contains(out, "triage is on") {
		t.Errorf("login claims triage is on: %q", out)
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
	if code, out := run("", "status"); code != 0 || !strings.Contains(out, "approvals suggest, triage off") {
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
	// With a command provider, logout says decisions keep running.
	config.SetKey(config.Path(), "decisions", "provider", config.Quote("command"))
	config.SetKey(config.Path(), "decisions", "command", `["my-classifier"]`)
	if code, out := run("", "logout"); code != 0 || !strings.Contains(out, "still run through your command (my-classifier)") || strings.Contains(out, "Decisions are off") {
		t.Fatalf("logout with a command provider: %d %q", code, out)
	}
	if p := config.LoadDecisions(config.Path()).Provider; p != "command" {
		t.Errorf("logout changed the command provider to %q", p)
	}
	// With decisions off, status makes no call and says so.
	config.SetKey(config.Path(), "decisions", "provider", config.Quote(""))
	decide.SaveKey(cred, key)
	pinged = ""
	if code, out := run("", "status"); code != 0 || pinged != "" || !strings.Contains(out, "decisions are off") || !strings.Contains(out, "not tested") {
		t.Fatalf("status with decisions off: %d %q, pinged %v", code, out, pinged != "")
	}
	if code, _ := run("", "nope"); code != 2 {
		t.Error("unknown subcommand accepted")
	}

	// The decisions log: status counts it, report reads it.
	if _, out := run("", "status"); !strings.Contains(out, "log:        no decisions logged yet") {
		t.Errorf("status without a log: %q", out)
	}
	if code, out := run("", "report"); code != 0 || !strings.Contains(out, "Nothing logged") {
		t.Errorf("empty report: %d %q", code, out)
	}
	j, err := decisionlog.Open(journalPath())
	if err != nil {
		t.Fatal(err)
	}
	j.Add(decisionlog.Event{T: time.Now().Add(-time.Hour), Kind: decisionlog.Call, ID: "a", Feature: "triage", Ms: 120, Tokens: 300, Answer: "now"})
	j.Add(decisionlog.Event{T: time.Now().AddDate(0, 0, -10), Kind: decisionlog.Call, ID: "b", Feature: "triage", Ms: 90})
	j.Close(time.Second)
	if _, out := run("", "status"); !strings.Contains(out, "log:        2 events since") {
		t.Errorf("status with a log: %q", out)
	}
	if code, out := run("", "report"); code != 0 || !strings.Contains(out, "1 calls") || !strings.Contains(out, "Verdict: not enough data yet") {
		t.Errorf("report: %d %q", code, out)
	}
	if code, out := run("", "report", "--days", "30"); code != 0 || !strings.Contains(out, "2 calls") {
		t.Errorf("report --days 30: %d %q", code, out)
	}
	if code, _ := run("", "report", "--days", "0"); code != 2 {
		t.Error("report --days 0 accepted")
	}
}
