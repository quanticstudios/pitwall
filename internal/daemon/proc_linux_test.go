package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// fakeProc is one process in a fake /proc tree.
type fakeProc struct {
	comm, exe, cmdline string
	pgrp               int
	children           string
	start              int // clock ticks after boot
}

// fakeProcRoot builds a /proc tree of procs under a temp dir and points
// procRoot at it for the test.
func fakeProcRoot(t *testing.T, procs map[int]fakeProc) {
	t.Helper()
	root := t.TempDir()
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		task := filepath.Join(dir, "task", strconv.Itoa(pid))
		must(t, os.MkdirAll(task, 0o755))
		must(t, os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p.cmdline), 0o644))
		// pid (comm) state ppid pgrp ...
		stat := strconv.Itoa(pid) + " (" + p.comm + ") S 1 " + strconv.Itoa(p.pgrp) + " 0" + strings.Repeat(" 0", 15) + " " + strconv.Itoa(p.start) + "\n"
		must(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
		must(t, os.WriteFile(filepath.Join(task, "children"), []byte(p.children), 0o644))
		if p.exe != "" {
			must(t, os.Symlink(p.exe, filepath.Join(dir, "exe")))
		}
	}
}

func TestProcIdentifyInterpreter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		procs map[int]fakeProc
		want  model.Provider
	}{
		{"npm gemini, relaunched", map[int]fakeProc{
			100: {comm: "node-MainThread", exe: "/usr/bin/node", cmdline: "node\x00/usr/bin/gemini\x00", pgrp: 100, children: "101"}, // node 26 names its main thread
			101: {comm: "node", exe: "/usr/bin/node", cmdline: "/usr/bin/node\x00--max-old-space-size=8192\x00/usr/bin/gemini\x00--api-key\x00secret\x00", pgrp: 100},
		}, model.ProviderGemini},
		{"cursor-agent behind its launcher", map[int]fakeProc{
			200: {comm: "cursor-agent", exe: "/usr/bin/bash", cmdline: "/bin/bash\x00/home/u/.local/bin/cursor-agent\x00", pgrp: 200, children: "201"},
		}, model.ProviderCursor},
		{"a launcher script running node", map[int]fakeProc{
			300: {comm: "sh", exe: "/usr/bin/sh", cmdline: "sh\x00./run\x00", pgrp: 300, children: "301"},
			301: {comm: "node", exe: "/usr/bin/node", cmdline: "node\x00/home/u/.local/share/cursor-agent/versions/1/index.js\x00", pgrp: 300},
		}, model.ProviderCursor},
		{"aider under env python", map[int]fakeProc{
			400: {comm: "python3", exe: "/usr/bin/python3.14", cmdline: "python3\x00/home/u/.local/bin/aider\x00", pgrp: 400},
		}, model.ProviderAider},
		{"a node server is no agent", map[int]fakeProc{
			500: {comm: "node", exe: "/usr/bin/node", cmdline: "node\x00server.js\x00gemini\x00", pgrp: 500},
		}, ""},
		{"argv is read only for interpreters", map[int]fakeProc{
			600: {comm: "make", exe: "/usr/bin/make", cmdline: "node\x00/usr/bin/gemini\x00", pgrp: 600},
		}, ""},
		{"another group's node is not read", map[int]fakeProc{
			700: {comm: "sh", exe: "/usr/bin/sh", pgrp: 700, children: "701"},
			701: {comm: "node", exe: "/usr/bin/node", cmdline: "node\x00/usr/bin/gemini\x00", pgrp: 799},
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeProcRoot(t, tc.procs)
			pg := 0
			for pid, p := range tc.procs {
				if pid == p.pgrp {
					pg = pid
				}
			}
			if got, _ := procIdentify(pg); got != tc.want {
				t.Errorf("procIdentify = %q, want %q", got, tc.want)
			}
		})
	}
}

// An agent with any process of its own is busy, an MCP server included,
// since nothing tells one from a dev server. A wrapper's agent counts as
// the agent.
func TestProcBusy(t *testing.T) {
	claude := fakeProc{comm: "claude", exe: "/usr/bin/claude", pgrp: 10, start: 1000}
	wrapper := fakeProc{comm: "node", exe: "/usr/bin/node", cmdline: "node\x00/usr/lib/node_modules/@openai/codex/bin/codex.js", pgrp: 10, start: 1000, children: "15"}
	codex := fakeProc{comm: "codex", exe: "/usr/lib/codex", pgrp: 10, start: 1010}
	for _, tc := range []struct {
		name  string
		procs map[int]fakeProc
		want  bool
	}{
		{"alone", map[int]fakeProc{10: claude}, false},
		{"a wrapper's agent alone", map[int]fakeProc{10: wrapper, 15: codex}, false},
		{"a shell", map[int]fakeProc{10: withChildren(claude, "13"), 13: {comm: "zsh", pgrp: 10, start: 1300}}, true},
		{"a node server exec'd 15s in", map[int]fakeProc{10: withChildren(claude, "14"), 14: {comm: "node", pgrp: 10, start: 1150}}, true},
		{"an MCP server from launch", map[int]fakeProc{10: withChildren(claude, "11"), 11: {comm: "node", pgrp: 10, start: 1001}}, true},
		{"a wrapper's agent's shell", map[int]fakeProc{10: wrapper, 15: withChildren(codex, "16"), 16: {comm: "bash", pgrp: 10, start: 1500}}, true},
		{"unreadable", map[int]fakeProc{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeProcRoot(t, tc.procs)
			if got := procBusy(10); got != tc.want {
				t.Fatalf("procBusy = %v, want %v", got, tc.want)
			}
		})
	}
}

func withChildren(p fakeProc, children string) fakeProc {
	p.children = children
	return p
}
