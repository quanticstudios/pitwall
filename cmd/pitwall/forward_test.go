//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestForwardEndsInSocket: pitwall's ssh forward of a host's daemon
// socket ends here in a unix socket in the private folder that holds
// proto.SocketPath, never on a TCP port, so the socket's control reaches
// no one the folder keeps out.
func TestForwardEndsInSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "pwf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin, run := filepath.Join(dir, "bin"), filepath.Join(dir, "run")
	log := filepath.Join(dir, "ssh.log")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >"+log+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("XDG_RUNTIME_DIR", run)
	r, err := newSSHHost("box", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.control("forward", "/run/user/1000/pitwall/pitwall.sock"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(data), "\n")
	i := strings.Index(string(data), "-L\n")
	if i < 0 {
		t.Fatalf("no -L in %q", args)
	}
	spec, _, _ := strings.Cut(string(data)[i+3:], "\n")
	local, remote, _ := strings.Cut(spec, ":")
	sock, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(local))
	if local != r.local || filepath.Dir(local) != filepath.Dir(sock) || !strings.HasSuffix(local, ".sock") ||
		err != nil || info.Mode().Perm() != 0o700 || remote != "/run/user/1000/pitwall/pitwall.sock" {
		t.Fatalf("forward %q: local %q in a folder of %v (%v), want a socket next to %s", spec, local, info.Mode(), err, sock)
	}
}
