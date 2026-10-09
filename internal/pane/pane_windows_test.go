package pane

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/vt"
)

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
