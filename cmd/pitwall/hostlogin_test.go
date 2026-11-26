//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const checkOutput = `printf '# Tailscale SSH requires an additional check.\r\n# To authenticate, visit: https://login.tailscale.com/a/1f2e3d4c5b6a7\r\n' >&2`

// scriptHost is an sshHost whose ssh is body, a sh script. $dir is a
// folder of its own.
func scriptHost(t *testing.T, body string) (*sshHost, string) {
	t.Helper()
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\ndir='"+dir+"'\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &sshHost{name: "box", dest: "box", ssh: ssh, dir: dir, local: filepath.Join(dir, "l.sock"), localNet: "unix"}, dir
}

func shortStart(t *testing.T) {
	prev := startTimeout
	t.Cleanup(func() { startTimeout = prev })
	startTimeout = time.Second
}

func TestStartSaysWhatSSHWaitsFor(t *testing.T) {
	shortStart(t)
	const fix = "pitwall host login -- 'box'"
	for _, c := range []struct {
		name, script, reason, url string
		detail                    []string
	}{
		{"tailscale check", checkOutput + "\nexec sleep 60", "Tailscale SSH wants an approval", "https://login.tailscale.com/a/1f2e3d4c5b6a7",
			[]string{"ssh to box timed out after 1s; ssh said:\n", "requires an additional check", "visit: https://login.tailscale.com/a/1f2e3d4c5b6a7"}},
		{"host key", "echo 'Host key verification failed.' >&2; exit 255", "ssh does not trust the host key", "",
			[]string{"unreachable: Host key verification failed."}},
		{"changed host key", "echo '@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @' >&2; exit 255", "ssh does not trust the host key", "", nil},
		{"password", "echo 'me@box: Permission denied (publickey,password).' >&2; exit 255", "ssh needs a password, key or code", "",
			[]string{"unreachable: me@box: Permission denied (publickey,password)."}},
		{"mfa", "echo 'me@box: Permission denied (keyboard-interactive).' >&2; exit 255", "ssh needs a password, key or code", "", nil},
		{"prompt", "printf 'API_TOKEN=example-secret\\nVerification code: ' >&2; exec sleep 60", "ssh is asking for a password or code", "",
			[]string{"timed out after 1s", "Verification code:"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := scriptHost(t, c.script)
			_, err := r.start(false)
			var e *loginError
			if !errors.As(err, &e) {
				t.Fatalf("%v", err)
			}
			if e.reason != c.reason || e.url != c.url || e.fix != fix || e.open == nil || !strings.HasSuffix(err.Error(), "\nfix: "+fix) {
				t.Fatalf("%+v", e)
			}
			for _, want := range c.detail {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q lacks %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "example-secret") {
				t.Fatalf("leaked: %q", err)
			}
		})
	}
}

// Failures nothing a person types would clear keep the normal retries, on
// one line with what ssh printed.
func TestStartPlainFailures(t *testing.T) {
	shortStart(t)
	for _, c := range []struct{ script, want string }{
		{"exec sleep 60", "ssh to box timed out after 1s"},
		{"echo 'debug: waiting' >&2; exec sleep 60", "ssh to box timed out after 1s; ssh said: debug: waiting"},
		{checkOutput + "; echo '# Authentication checked with Tailscale SSH.' >&2; exec sleep 60", "# Authentication checked"},
		{"echo 'ssh: connect to host box port 22: No route to host' >&2; exit 255", "cannot reach box over ssh: ssh: connect to host box port 22: No route to host"},
	} {
		r, _ := scriptHost(t, c.script)
		_, err := r.start(false)
		var e *loginError
		if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: %q", c.script, err)
		}
	}
}

func TestSharedCheck(t *testing.T) {
	r, dir := scriptHost(t, `echo "$*" >"$dir/args"; test -e "$dir/up"`)
	open := r.sharedCheck()
	if open() {
		t.Fatal("open with no shared connection")
	}
	os.WriteFile(filepath.Join(dir, "up"), nil, 0o600)
	if !open() {
		t.Fatal("not open")
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); !strings.Contains(string(args), "ControlPath=") || !strings.Contains(string(args), "-O check -- box") {
		t.Fatalf("%s", args)
	}
}
