package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// queueState is cliState with two tasks queued in main and one in api.
func queueState() model.State {
	st := cliState()
	st.Tasks = []model.Task{
		{ID: "t1", SessionID: "m", GroupID: "g", Dir: "/work/a", Worktree: "flaky", Cmd: []string{"/bin/codex", "fix the flaky test\nthen push"}},
		{ID: "t2", SessionID: "s2", Dir: "/work/z", Cmd: []string{"/bin/claude", "elsewhere"}},
		{ID: "t3", SessionID: "m", Dir: "/work/c", Cmd: []string{"/bin/pi"}},
	}
	return st
}

func TestCLIQueue(t *testing.T) {
	t.Run("ls", func(t *testing.T) {
		fakeCLI(t, cliExchange{state: queueState()})
		code, out, stderr := cliOutput("queue", "ls")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if code != 0 || stderr != "" || len(lines) != 3 ||
			strings.Join(strings.Fields(lines[1]), " ") != "1 fix the flaky test /work/a (new worktree flaky) agents" ||
			strings.Join(strings.Fields(lines[2]), " ") != "2 pi /work/c -" {
			t.Fatalf("%d: %q %s", code, out, stderr)
		}
	})
	t.Run("add", func(t *testing.T) {
		bin := t.TempDir()
		agent := filepath.Join(bin, exeName("fake-agent"))
		if err := os.WriteFile(agent, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		dir := t.TempDir()
		t.Chdir(dir)
		want := proto.NewTask{Task: model.Task{SessionID: "m", Dir: dir, Cmd: []string{agent, "fix it"}}, Queue: true}
		fakeCLI(t, cliExchange{state: queueState()}, cliExchange{request: want, state: queueState()})
		if code, out, stderr := cliOutput("queue", "add", "--", "fake-agent", "fix it"); code != 0 || out != "" || stderr != "" {
			t.Fatalf("%d: %s %s", code, out, stderr)
		}
	})
	t.Run("add an issue", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fake gh is a shell script")
		}
		bin := t.TempDir()
		agent := filepath.Join(bin, "fake-agent")
		gh := "#!/bin/sh\n[ \"$*\" = 'issue view 12 --json number,title,url,body' ] || exit 2\necho '{\"number\":12,\"title\":\"Fix login\",\"url\":\"https://github.com/o/r/issues/12\",\"body\":\"It loops.\"}'\n"
		for f, script := range map[string]string{agent: "#!/bin/sh\n", filepath.Join(bin, "gh"): gh} {
			if err := os.WriteFile(f, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin)
		dir := t.TempDir()
		t.Chdir(dir)
		prompt := "GitHub issue #12: Fix login\nhttps://github.com/o/r/issues/12\n\nIt loops."
		want := proto.NewTask{Task: model.Task{SessionID: "m", Dir: dir, Cmd: []string{agent, "-s", "read-only", prompt}}, Queue: true}
		fakeCLI(t, cliExchange{state: queueState()}, cliExchange{request: want, state: queueState()})
		if code, out, stderr := cliOutput("queue", "add", "--issue", "12", "--", "fake-agent", "-s", "read-only"); code != 0 || out != "" || stderr != "" {
			t.Fatalf("%d: %s %s", code, out, stderr)
		}
	})
	t.Run("rm counts in the session", func(t *testing.T) {
		fakeCLI(t, cliExchange{state: queueState()}, cliExchange{request: proto.DropTask{ID: "t3"}, state: queueState()})
		if code, out, stderr := cliOutput("queue", "rm", "2"); code != 0 || out != "" || stderr != "" {
			t.Fatalf("%d: %s %s", code, out, stderr)
		}
	})
	t.Run("rm out of range", func(t *testing.T) {
		fakeCLI(t, cliExchange{state: queueState()})
		if code, _, stderr := cliOutput("queue", "rm", "3"); code != 1 || !strings.Contains(stderr, "no queued task 3") {
			t.Fatalf("%d: %s", code, stderr)
		}
	})
}
