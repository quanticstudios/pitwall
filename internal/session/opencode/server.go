package opencode

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/childproc"
	"github.com/quanticstudios/pitwall/internal/session"
)

// CommandEnv overrides the opencode executable, as for the other drivers.
const CommandEnv = "PITWALL_OPENCODE_COMMAND"

// stderrTail keeps the last 4 KiB of opencode serve's stderr for startup errors.
type stderrTail struct {
	mu sync.Mutex
	b  []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - 4096; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// serve uses OpenCode's automatic loopback port selection and reads the
// SDK's readiness line. No shell or port reservation is needed.
func serve(ctx context.Context, cfg session.Config) (string, string, func(context.Context), error) {
	name := os.Getenv(CommandEnv)
	if name == "" {
		name = "opencode"
	}
	procCtx, procCancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, name, "serve", "--hostname=127.0.0.1", "--port=0")
	childproc.Bound(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = cfg.Cwd
	password := rand.Text()
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Env = append(cmd.Env, "OPENCODE_SERVER_USERNAME=opencode", "OPENCODE_SERVER_PASSWORD="+password)
	pr, pw, err := os.Pipe()
	if err != nil {
		procCancel()
		return "", "", nil, err
	}
	cmd.Stdout = pw
	stderr := &stderrTail{}
	cmd.Stderr = stderr
	err = ctx.Err()
	if err == nil {
		err = cmd.Start()
	}
	pw.Close()
	if err != nil {
		pr.Close()
		procCancel()
		return "", "", nil, err
	}
	ready := make(chan string, 1)
	read := make(chan struct{})
	go func() {
		defer close(read)
		s := bufio.NewScanner(pr)
		s.Buffer(make([]byte, 64<<10), 4<<20)
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
		defer procCancel()
		cmd.Wait()
		select {
		case <-read:
		case <-time.After(2 * time.Second):
			pr.Close()
			<-read
		}
		pr.Close()
		close(exited)
	}()
	var once sync.Once
	stop := func(ctx context.Context) {
		unhook := context.AfterFunc(ctx, procCancel)
		defer unhook()
		once.Do(func() {
			// Windows does not implement os.Interrupt for child processes.
			if cmd.Process.Signal(os.Interrupt) != nil {
				procCancel()
			}
			select {
			case <-exited:
			case <-ctx.Done():
				procCancel()
				<-exited
			case <-time.After(2 * time.Second):
				procCancel()
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
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", "", nil, fmt.Errorf("opencode serve exited before readiness: %s", msg)
		}
		return "", "", nil, fmt.Errorf("opencode serve exited before readiness")
	}
}
