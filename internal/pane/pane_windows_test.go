package pane

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	ossignal "os/signal"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestMain doubles as the programs the tests run in a pane: a parent that
// starts a stubborn child on its console and waits for it, and an echoer.
func TestMain(m *testing.M) {
	switch os.Getenv("PITWALL_PANE_TEST") {
	case "echo":
		fmt.Println("echo ready")
		for in := bufio.NewScanner(os.Stdin); in.Scan(); {
			fmt.Println("got", in.Text())
		}
		os.Exit(0)
	case "parent":
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), "PITWALL_PANE_TEST=stubborn")
		c.Stdout = os.Stdout
		c.Run()
		os.Exit(0)
	case "stubborn":
		// The runtime holds the console's close event while SIGTERM is
		// notified, until Windows times the process out.
		ossignal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
		fmt.Println("ready")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestCloseKillsDescendants: a descendant that holds off the console's close
// event keeps conhost, and with it the pane, alive past its parent. Close
// kills it on the 2s path instead of waiting out Windows' close timeout.
func TestCloseKillsDescendants(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(Config{ID: "tree", Cmd: []string{exe}, Env: []string{"PITWALL_PANE_TEST=parent"}, Cols: 80, Rows: 24, NewVT: vt.New})
	if err != nil {
		t.Fatal(err)
	}
	waitScreen(t, p, "ready")
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Fatal("Close did not kill the pane's descendants on the 2s path")
	}
}

// TestInputAfterResize: a console program reads what is typed after its
// pane resizes, and the resize ends nothing. The program is not Git's sh:
// MSYS ends a waiting read on a resize, so its while-read loop exits.
func TestInputAfterResize(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(Config{ID: "resize", Cmd: []string{exe}, Env: []string{"PITWALL_PANE_TEST=echo"}, Cols: 80, Rows: 24, NewVT: vt.New})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	waitScreen(t, p, "echo ready")
	if _, err := p.Write([]byte("before\r")); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, p, "got before")
	if err := p.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("after\r")); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, p, "got after")
	if code := p.ExitCode(); code != -1 {
		t.Fatalf("the resize ended the program: exit %d", code)
	}
}

// waitScreen waits for text on p's screen.
func waitScreen(t *testing.T, p *Pane, text string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(screen(p), text); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %q; screen:\n%s", text, screen(p))
		}
	}
}

func TestReportCwdArgs(t *testing.T) {
	argv, env := reportCwd([]string{`C:\Program Files\PowerShell\7\pwsh.exe`}, []string{"A=1"})
	if len(argv) != 4 || argv[1] != "-NoExit" || argv[3] != psPrompt || len(env) != 1 {
		t.Fatalf("pwsh: %q %q", argv, env)
	}
	argv, env = reportCwd([]string{"cmd.exe"}, []string{"Prompt=$N$G", "A=1"})
	if len(argv) != 1 || !slices.Equal(env, []string{"A=1", `PROMPT=$E]7;file://localhost/$P$E\$N$G`}) {
		t.Fatalf("cmd: %q %q", argv, env)
	}
	argv, env = reportCwd([]string{"bash.exe"}, []string{"A=1"})
	if len(argv) != 1 || len(env) != 1 {
		t.Fatalf("bash: %q %q", argv, env)
	}
}

// screen is p's screen as text, for a failure message.
func screen(p *Pane) string {
	g := p.Snapshot()
	var b strings.Builder
	for y := range g.Rows {
		for x := range g.Cols {
			b.WriteString(g.At(x, y).Content)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestCwdFollowsCd types cd into each shell pitwall injects OSC 7 into and
// waits for Cwd to follow.
func TestCwdFollowsCd(t *testing.T) {
	for _, shell := range []string{"powershell.exe", "pwsh.exe", "cmd.exe"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skip(shell + " is not installed")
			}
			t.Setenv("SHELL", shell)
			p, err := Start(Config{ID: "cwd", Cwd: t.TempDir(), Cols: 80, Rows: 24, NewVT: vt.New})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			wait := func(what string, ok func(string) bool) {
				t.Helper()
				for deadline := time.Now().Add(30 * time.Second); !ok(p.Cwd()); time.Sleep(100 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("%s: Cwd = %q; screen:\n%s", what, p.Cwd(), screen(p))
					}
				}
			}
			wait("first prompt", func(c string) bool { return c != "" })
			if _, err := p.Write([]byte(`cd C:\Windows` + "\r")); err != nil {
				t.Fatal(err)
			}
			wait("after cd", func(c string) bool { return strings.EqualFold(c, `C:\Windows`) })
		})
	}
}
