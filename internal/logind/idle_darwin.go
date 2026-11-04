package logind

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
)

// InhibitIdle blocks idle sleep until the lock closes or this daemon exits.
func InhibitIdle(ctx context.Context) (io.Closer, error) {
	// -w also releases the assertion if the daemon exits without cleanup.
	cmd := exec.CommandContext(ctx, "/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &idleProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type idleProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *idleProcess) Close() error {
	p.cmd.Process.Kill()
	<-p.done
	return p.err
}

func (p *idleProcess) Err() error {
	select {
	case <-p.done:
		if p.err != nil {
			return p.err
		}
		return errors.New("caffeinate exited")
	default:
		return nil
	}
}
