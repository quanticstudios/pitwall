package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
			strings.Join(strings.Fields(lines[1]), " ") != "1 fix the flaky test /work/a (new worktree flaky) agents - -" ||
			strings.Join(strings.Fields(lines[2]), " ") != "2 pi /work/c - - -" {
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

func TestQueueScheduleFlags(t *testing.T) {
	for _, c := range []struct{ name, at, every string }{
		{"at", "02:00", ""}, {"every", "", "24h"}, {"both", "02:00", "24h"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			bin := t.TempDir()
			agent := filepath.Join(bin, exeName("fake-agent"))
			if err := os.WriteFile(agent, []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			next, repeat, err := model.ParseTaskSchedule(c.at, c.every, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if c.at == "" {
				// Verify duration flags through a rejected older daemon, before a task
				// could be sent with an ignored schedule.
				fakeCLI(t, cliExchange{state: queueState(), level: proto.ScheduleLevel - 1})
				code, _, stderr := cliOutput("queue", "add", "--every", c.every, "--", "fake-agent", "nightly")
				if code != 1 || !strings.Contains(stderr, "newer daemon") {
					t.Fatalf("%d: %s", code, stderr)
				}
				return
			}
			want := proto.NewTask{Task: model.Task{SessionID: "m", Dir: dir, Cmd: []string{agent, "nightly"}, NextStart: next, Repeat: repeat}, Queue: true}
			fakeCLI(t, cliExchange{state: queueState(), level: proto.Level}, cliExchange{request: want, state: queueState(), level: proto.Level})
			args := []string{"queue", "add", "--at", c.at}
			if c.every != "" {
				args = append(args, "--every", c.every)
			}
			args = append(args, "--", "fake-agent", "nightly")
			if code, _, stderr := cliOutput(args...); code != 0 {
				t.Fatalf("%d: %s", code, stderr)
			}
		})
	}
	for _, args := range [][]string{{"--at", "25:00"}, {"--every", "0s"}, {"--every", "nonsense"}} {
		args = append([]string{"queue", "add"}, args...)
		args = append(args, "--", "fake-agent", "nightly")
		if code, _, _ := cliOutput(args...); code != 1 {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestListScheduledTask(t *testing.T) {
	var out strings.Builder
	next := time.Date(2026, 10, 30, 2, 0, 0, 0, time.Local)
	task := model.Task{Cmd: []string{"codex", "nightly"}, NextStart: next, Repeat: 24 * time.Hour}
	if err := listTasks(&out, model.State{}, []model.Task{task}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2026-10-30 02:00") || !strings.Contains(out.String(), "24h0m0s") {
		t.Fatal(out.String())
	}
}
