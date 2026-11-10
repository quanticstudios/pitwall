package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/quanticstudios/pitwall/internal/model"
)

// fakeProc is one process in a fake /proc tree.
type fakeProc struct {
	comm, exe, cmdline string
	pgrp               int
	children           string
	start              int            // clock ticks after boot
	fds                map[int]string // fd: where it points, as "pipe:[1]"; nil for no fd directory
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
		if p.fds != nil {
			must(t, os.MkdirAll(filepath.Join(dir, "fd"), 0o755))
		}
		for fd, target := range p.fds {
			must(t, os.Symlink(target, filepath.Join(dir, "fd", strconv.Itoa(fd))))
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

// An agent is busy with any process but its stdio servers: a child whose
// stdin and stdout are pipes the agent holds the other ends of, as an MCP
// server's are. A wrapper's agent counts as the agent, never an agent
// deeper in the tree. Anything unreadable is busy.
func TestProcBusy(t *testing.T) {
	held := map[int]string{3: "pipe:[100]", 4: "pipe:[101]", 5: "pipe:[102]"}
	claude := fakeProc{comm: "claude", exe: "/usr/bin/claude", pgrp: 10, fds: held}
	mcp := fakeProc{comm: "node", pgrp: 10, fds: map[int]string{0: "pipe:[100]", 1: "pipe:[101]", 2: "/dev/null"}}
	tool := fakeProc{comm: "zsh", pgrp: 10, fds: map[int]string{0: "/dev/null", 1: "/tmp/tasks/b1.output", 2: "/tmp/tasks/b1.output"}}
	wrapper := fakeProc{comm: "node", exe: "/usr/bin/node", cmdline: "node\x00/usr/lib/node_modules/@openai/codex/bin/codex.js", pgrp: 10, children: "15", fds: map[int]string{}}
	codex := fakeProc{comm: "codex", exe: "/usr/lib/codex", pgrp: 10, fds: map[int]string{3: "pipe:[200]", 4: "pipe:[201]"}}
	codexMCP := fakeProc{comm: "node", pgrp: 10, fds: map[int]string{0: "pipe:[200]", 1: "pipe:[201]"}}
	for _, tc := range []struct {
		name  string
		procs map[int]fakeProc
		want  bool
	}{
		{"alone", map[int]fakeProc{10: claude}, false},
		{"MCP stdio servers", map[int]fakeProc{10: withChildren(claude, "11 12"), 11: mcp, 12: mcp}, false},
		{"a background tool writing to a file", map[int]fakeProc{10: withChildren(claude, "11 13"), 11: mcp, 13: tool}, true},
		{"a hook's dev server on pipes of its own", map[int]fakeProc{10: withChildren(claude, "14"), 14: {comm: "node", pgrp: 10, fds: map[int]string{0: "pipe:[900]", 1: "pipe:[901]"}}}, true},
		{"only its stdin held", map[int]fakeProc{10: withChildren(claude, "14"), 14: {comm: "node", pgrp: 10, fds: map[int]string{0: "pipe:[100]", 1: "/tmp/server.log"}}}, true},
		{"a child's fds unreadable", map[int]fakeProc{10: withChildren(claude, "14"), 14: {comm: "node", pgrp: 10}}, true},
		{"the agent's fds unreadable", map[int]fakeProc{10: {comm: "claude", exe: "/usr/bin/claude", pgrp: 10, children: "11"}, 11: mcp}, true},
		{"a Codex started as a tool", map[int]fakeProc{10: withChildren(claude, "15"), 15: {comm: "codex", exe: "/usr/lib/codex", pgrp: 10, fds: map[int]string{0: "/dev/null", 1: "pipe:[950]"}}}, true},
		{"a wrapper's agent with an MCP server", map[int]fakeProc{10: wrapper, 15: withChildren(codex, "16"), 16: codexMCP}, false},
		{"a wrapper's agent's tool", map[int]fakeProc{10: wrapper, 15: withChildren(codex, "17"), 17: tool}, true},
		{"a wrapper running more than its agent", map[int]fakeProc{10: withChildren(wrapper, "15 18"), 15: codex, 18: codexMCP}, true},
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

// stdioOnly on real pipes, with the test process as the agent: a child on
// two pipes the test holds the other ends of is a stdio server; one on
// /dev/null, or on pipes nobody else holds, is not.
func TestStdioOnlyRealPipes(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	start := func(stdin, stdout *os.File) int {
		t.Helper()
		cmd := exec.Command(sleep, "30")
		cmd.Stdin, cmd.Stdout = stdin, stdout
		must(t, cmd.Start())
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		return cmd.Process.Pid
	}
	me := os.Getpid()
	inR, inW, err := os.Pipe()
	must(t, err)
	outR, outW, err := os.Pipe()
	must(t, err)
	t.Cleanup(func() { inW.Close(); outR.Close() })
	server := start(inR, outW)
	inR.Close()
	outW.Close()
	if !stdioOnly(me, []int{server}) {
		t.Fatal("a child on pipes the agent holds is not a stdio server")
	}
	quiet := start(nil, nil) // /dev/null, as a background tool's redirect
	if stdioOnly(me, []int{server, quiet}) {
		t.Fatal("a child on /dev/null counts as a stdio server")
	}
	aR, aW, err := os.Pipe()
	must(t, err)
	bR, bW, err := os.Pipe()
	must(t, err)
	own := start(aR, bW)
	for _, f := range []*os.File{aR, aW, bR, bW} {
		f.Close()
	}
	if stdioOnly(me, []int{own}) {
		t.Fatal("a child on pipes the agent does not hold counts as a stdio server")
	}
	// Node gives a child's stdio a socketpair: the two ends' inodes differ.
	pair := func() (mine, theirs *os.File) {
		t.Helper()
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		must(t, err)
		return os.NewFile(uintptr(fds[0]), "mine"), os.NewFile(uintptr(fds[1]), "theirs")
	}
	inMine, inTheirs := pair()
	outMine, outTheirs := pair()
	t.Cleanup(func() { inMine.Close(); outMine.Close() })
	node := start(inTheirs, outTheirs)
	inTheirs.Close()
	outTheirs.Close()
	if !stdioOnly(me, []int{server, node}) {
		t.Fatal("a child on socketpairs the agent holds the other ends of is not a stdio server")
	}
	aMine, aTheirs := pair()
	bMine, bTheirs := pair()
	gone := start(aTheirs, bTheirs)
	for _, f := range []*os.File{aMine, aTheirs, bMine, bTheirs} {
		f.Close()
	}
	if stdioOnly(me, []int{gone}) {
		t.Fatal("a child on sockets whose peers the agent does not hold counts as a stdio server")
	}
}

func withChildren(p fakeProc, children string) fakeProc {
	p.children = children
	return p
}
