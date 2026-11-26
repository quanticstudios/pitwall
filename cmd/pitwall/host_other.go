//go:build !windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// control asks the shared connection to forward or cancel the forward of
// sock on the host to r.local.
func (r *sshHost) control(op, sock string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, r.ssh, r.sshArgs([]string{"-O", op, "-L", r.local + ":" + sock})...).CombinedOutput()
	if err != nil {
		if msg := lastLine(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// sharedCheck reports whether the shared connection is up. It asks the
// local ssh alone, so it never prompts and never reaches the host.
func (r *sshHost) sharedCheck() func() bool {
	return func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, r.ssh, r.sshArgs([]string{"-O", "check"})...).Run() == nil
	}
}
