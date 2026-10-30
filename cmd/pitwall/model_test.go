package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestCLIModel(t *testing.T) {
	for _, command := range []string{"new", "queue"} {
		for _, agent := range []string{"claude", "codex"} {
			for _, selection := range []string{"custom-model", "default"} {
				t.Run(command+"/"+agent+"/"+selection, func(t *testing.T) {
					bin := t.TempDir()
					path := filepath.Join(bin, exeName(agent))
					if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", bin)
					dir := t.TempDir()
					t.Chdir(dir)
					args := []string{command}
					if command == "queue" {
						args = append(args, "add")
					}
					args = append(args, "--model", selection, "--", agent, "fix it")
					cmd := []string{path, "fix it"}
					if selection != "default" {
						flag := "--model"
						if agent == "codex" {
							flag = "-m"
						}
						cmd = []string{path, flag, selection, "fix it"}
					}
					before, after := cliState(), cliState()
					after.Workspaces = append(after.Workspaces, model.Workspace{ID: "new", SessionID: "m"})
					var want any = proto.NewSession{Cwd: dir, SessionID: "m", Cmd: cmd}
					if command == "queue" {
						want = proto.NewTask{Task: model.Task{SessionID: "m", Dir: dir, Cmd: cmd}, Queue: true}
					}
					fakeCLI(t, cliExchange{state: before}, cliExchange{request: want, state: after})
					if code, _, stderr := cliOutput(args...); code != 0 || stderr != "" {
						t.Fatalf("%d: %s", code, stderr)
					}
				})
			}
		}
	}
	for _, args := range [][]string{{"new", "--model", "opus"}, {"queue", "add", "--model", "opus"}} {
		if code, _, stderr := cliOutput(args...); code != 1 || !strings.Contains(stderr, "--model needs an agent") {
			t.Fatalf("%v: %d: %s", args, code, stderr)
		}
	}
}
