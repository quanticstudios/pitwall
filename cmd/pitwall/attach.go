package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/quanticstudios/pitwall/internal/proto"
)

var launchGUI = func(workspaceID string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "PITWALL_ATTACH="+workspaceID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// guiLock returns nil when another window holds the lock. Closing releases it.
func guiLock() (*os.File, error) {
	path, err := proto.SocketPath()
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), "gui.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return lock, nil
}

func attachGUI(workspaceID string) error {
	lock, err := guiLock()
	if err != nil || lock == nil {
		return err
	}
	// The child acquires the lock in runGUI; concurrent children exit there.
	if err := lock.Close(); err != nil {
		return err
	}
	return launchGUI(workspaceID)
}
