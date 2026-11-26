//go:build windows

package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"github.com/quanticstudios/pitwall/internal/nowindow"
	"golang.org/x/sys/windows"
)

// control is Windows' forward: its ssh cannot add or drop a forward on an
// existing connection, so "forward" runs a dedicated ssh -N -L that holds
// it, and "cancel" kills that process.
func (r *sshHost) control(op, sock string) error {
	switch op {
	case "forward":
		return r.forwardStart(sock)
	case "cancel":
		r.forwardStop()
		return nil
	}
	return fmt.Errorf("control %q", op)
}

// sharedCheck is nil: Windows' ssh shares no connection to watch for.
func (r *sshHost) sharedCheck() func() bool { return nil }

// forwardStart runs ssh -N -L r.local:sock and returns once the forwarded
// port answers a TCP connect, or ssh exits. An earlier forward process of
// this sshHost, live or dead, is stopped first.
func (r *sshHost) forwardStart(sock string) error {
	r.forwardStop()
	// why: ExitOnForwardFailure makes a bind failure (the port got taken
	// between our probe and ssh's bind) exit the process instead of
	// leaving a connection without the forward.
	cmd := exec.Command(r.ssh, r.sshArgs([]string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", r.local + ":" + sock})...)
	nowindow.Set(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("run ssh: %w", err)
	}
	// why: Windows kills no child when its parent dies, so a window that
	// crashes or is killed would leave this ssh -N holding the forward for
	// good. The job kills it when this process exits for any reason.
	if err := jobAssign(killJob(), cmd); err != nil {
		log.Printf("ssh forward stays after a crash: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case err := <-done:
			msg := lastLine(stderr.String())
			if msg == "" && err != nil {
				msg = err.Error()
			}
			return fmt.Errorf("ssh forward exited: %s", msg)
		default:
		}
		// A probe connect reaches the host's daemon through the forward and
		// disconnects without a Hello, which the daemon tolerates.
		if c, err := net.DialTimeout("tcp", r.local, 200*time.Millisecond); err == nil {
			c.Close()
			r.mu.Lock()
			r.fwd = cmd
			r.mu.Unlock()
			return nil
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			<-done
			return fmt.Errorf("ssh forward of %s to %s did not come up", sock, r.local)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// forwardStop kills the forward's ssh process when one runs. The wait
// goroutine from forwardStart reaps it.
func (r *sshHost) forwardStop() {
	r.mu.Lock()
	cmd := r.fwd
	r.fwd = nil
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

var jobOnce sync.Once
var job windows.Handle

// killJob is this process's job object, made once, whose processes Windows
// kills when this process's last job handle closes: always, on exit.
func killJob() windows.Handle {
	jobOnce.Do(func() {
		j, err := newKillJob()
		if err != nil {
			log.Printf("no job object: %v", err)
			return
		}
		job = j
	})
	return job
}

// newKillJob is a job object with KILL_ON_JOB_CLOSE.
func newKillJob() (windows.Handle, error) {
	j, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(j, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(j)
		return 0, err
	}
	return j, nil
}

// jobAssign puts cmd's process in job j. A nil job fails, as a process
// already inside a job that forbids nesting may: callers log and go on.
func jobAssign(j windows.Handle, cmd *exec.Cmd) error {
	if j == 0 {
		return fmt.Errorf("no job")
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(j, h)
}
