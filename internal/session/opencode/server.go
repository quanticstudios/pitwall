package opencode

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// CommandEnv overrides the opencode executable, as for the other drivers.
const CommandEnv = "PITWALL_OPENCODE_COMMAND"

// serve uses OpenCode's automatic loopback port selection and reads the
// SDK's readiness line. No shell or port reservation is needed.
func serve(ctx context.Context, cfg session.Config) (string, string, func(context.Context), error) {
	name := os.Getenv(CommandEnv)
	if name == "" {
		name = "opencode"
	}
	cmd := exec.Command(name, "serve", "--hostname=127.0.0.1", "--port=0")
	cmd.Dir = cfg.Cwd
	password := rand.Text()
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Env = append(cmd.Env, "OPENCODE_SERVER_USERNAME=opencode", "OPENCODE_SERVER_PASSWORD="+password)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", nil, err
	}
	// Drain stderr without retaining prompts, credentials or provider output.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return "", "", nil, err
	}
	ready := make(chan string, 1)
	read := make(chan struct{})
	go func() {
		defer close(read)
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			if u, ok := strings.CutPrefix(s.Text(), "opencode server listening on "); ok {
				select {
				case ready <- strings.TrimSpace(u):
				default:
				}
			}
		}
	}()
	exited := make(chan struct{})
	go func() {
		<-read
		cmd.Wait()
		close(exited)
	}()
	var once sync.Once
	stop := func(ctx context.Context) {
		unhook := context.AfterFunc(ctx, func() { cmd.Process.Kill() })
		defer unhook()
		once.Do(func() {
			// Windows does not implement os.Interrupt for child processes.
			if cmd.Process.Signal(os.Interrupt) != nil {
				cmd.Process.Kill()
			}
			select {
			case <-exited:
			case <-ctx.Done():
				cmd.Process.Kill()
				stdout.Close()
				<-exited
			case <-time.After(2 * time.Second):
				cmd.Process.Kill()
				stdout.Close()
				<-exited
			}
		})
	}
	select {
	case u := <-ready:
		return u, password, stop, nil
	case <-ctx.Done():
		stop(ctx)
		return "", "", nil, ctx.Err()
	case <-exited:
		stop(ctx)
		return "", "", nil, fmt.Errorf("opencode serve exited before readiness")
	}
}
