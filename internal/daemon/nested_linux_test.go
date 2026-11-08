package daemon

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPeerPID(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	must(t, err)
	defer ln.Close()
	go func() {
		if c, err := net.Dial("unix", ln.Addr().String()); err == nil {
			defer c.Close()
			c.Read(make([]byte, 1))
		}
	}()
	nc, err := ln.Accept()
	must(t, err)
	defer nc.Close()
	if got := peerPID(nc); got != os.Getpid() {
		t.Fatalf("peerPID = %d, want %d", got, os.Getpid())
	}
	if ppid, pg := procParent(os.Getpid()); ppid != os.Getppid() || pg != syscall.Getpgrp() {
		t.Fatalf("procParent = %d, %d, want %d, %d", ppid, pg, os.Getppid(), syscall.Getpgrp())
	}
}
