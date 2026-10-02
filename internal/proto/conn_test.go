package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func bigGrid(cols, rows int) vt.Grid {
	g := vt.Grid{Cols: cols, Rows: rows, Cells: make([]vt.Cell, cols*rows),
		Cursor: vt.Cursor{X: 3, Y: 4, Visible: true, Shape: vt.CursorBar}, Title: "zsh", AltScreen: true}
	for i := range g.Cells {
		g.Cells[i] = vt.Cell{Content: string(rune('a' + i%26)), Width: 1,
			FG: vt.PaletteFlag | vt.Color(i%256), BG: vt.RGBFlag | 0x102030, Attrs: vt.Bold}
	}
	return g
}

func allMessages() []any {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	tree := &layout.Node{Dir: layout.Vertical, Ratios: []float64{0.5, 0.5},
		Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}}}
	st := model.State{
		Version:    7,
		Projects:   []model.Project{{ID: "p", Name: "n", Root: "/r", Kind: model.ProjectGit, Color: "red"}},
		Workspaces: []model.Workspace{{ID: "w", ProjectID: "p", Name: "x", Branch: "x", Path: "/r/x", UpdatedAt: now, Layout: tree}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cmd: []string{"claude"}, Cwd: "/r/x", Exited: true, ExitCode: 2, Provider: model.ProviderClaude, SessionID: "s"}},
		Activities: []model.Activity{{PaneID: "a", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: now}},
		Stats:      map[string]model.BranchStats{"w": {Additions: 1, Deletions: 2, MergeStatus: model.MergeClean, Ahead: 3, Behind: 4}},
	}
	return []any{
		Hello{Version: Version, Kind: "gui"},
		Input{Pane: "a", Data: []byte("ls\r")},
		Resize{Pane: "a", Cols: 80, Rows: 24},
		AddProject{Path: "/r"},
		NewWorkspace{ProjectID: "p", Name: "x"},
		RenameWorkspace{WorkspaceID: "w", Name: "y"},
		ArchiveWorkspace{WorkspaceID: "w", Archived: true},
		DeleteWorkspace{WorkspaceID: "w", RemoveBranch: true},
		OpenPane{WorkspaceID: "w", Target: "a", Dir: layout.Vertical, Cmd: []string{"codex"}},
		Scroll{Pane: "a", Lines: -3},
		ClosePane{Pane: "a"},
		SetLayout{WorkspaceID: "w", Layout: tree},
		AgentEvent{Pane: "a", Provider: model.ProviderCodex, Payload: []byte(`{"x":1}`)},
		StateMsg{State: st},
		Frame{Pane: "a", Grid: bigGrid(200, 60), Modes: vt.Modes{AppCursorKeys: true, Mouse: vt.MouseAny, MouseSGR: true, KittyKeyboard: 3}},
		PaneExited{Pane: "a", ExitCode: 1},
		Error{Message: "boom"},
	}
}

func TestRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	ca, cb := NewConn(a), NewConn(b)
	defer ca.Close()
	defer cb.Close()
	msgs := allMessages()
	if len(msgs) != len(Messages) {
		t.Fatalf("test covers %d of %d message types", len(msgs), len(Messages))
	}
	msgs = append(msgs, msgs...)
	go func() {
		for _, m := range msgs {
			if err := ca.Send(m); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for _, want := range msgs {
		got, err := cb.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%T round trip:\n got %+v\nwant %+v", want, got, want)
		}
	}
}

func TestRecvRejectsOversizedFrame(t *testing.T) {
	for _, size := range []uint32{64<<20 + 1, ^uint32(0)} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			b.SetReadDeadline(time.Now().Add(time.Second))
			c := NewConn(b)
			go func() {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], size)
				a.Write(header[:])
			}()
			if _, err := c.Recv(); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("Recv() = %v; want size rejection before reading a body", err)
			}
		})
	}
}

func TestRecvTruncatedFrame(t *testing.T) {
	for _, data := range [][]byte{{0, 0, 0}, {0, 0, 0, 10, 1, 2, 3}} {
		t.Run(fmt.Sprint(data), func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			go func() {
				defer a.Close()
				a.Write(data)
			}()
			if _, err := NewConn(b).Recv(); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("Recv() = %v; want a truncated frame error", err)
			}
		})
	}
}

func TestConcurrentSend(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetDeadline(time.Now().Add(5 * time.Second))
	b.SetDeadline(time.Now().Add(5 * time.Second))
	sender, receiver := NewConn(a), NewConn(b)
	const n = 32
	errs := make(chan error, n)
	for i := range n {
		go func() { errs <- sender.Send(Input{Pane: fmt.Sprint(i), Data: []byte("input")}) }()
	}
	seen := make(map[string]bool)
	for range n {
		m, err := receiver.Recv()
		if err != nil {
			t.Fatal(err)
		}
		input, ok := m.(Input)
		if !ok || seen[input.Pane] || string(input.Data) != "input" {
			t.Fatalf("unexpected message: %#v", m)
		}
		seen[input.Pane] = true
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestFrameSize(t *testing.T) {
	plain := bigGrid(200, 60)
	for i := range plain.Cells {
		plain.Cells[i] = vt.Cell{Content: "a", Width: 1}
	}
	frameSize(t, "colored", bigGrid(200, 60))
	frameSize(t, "plain", plain)
}

func frameSize(t *testing.T, name string, g vt.Grid) {
	f := Frame{Pane: "0123456789abcdef", Grid: g}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(envelope{f}); err != nil {
		t.Fatal(err)
	}
	first := buf.Len()
	buf.Reset()
	start := time.Now()
	const n = 20
	for range n {
		buf.Reset()
		if err := enc.Encode(envelope{f}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%s 200x60 Frame: first %d bytes (with type info), then %d bytes, encode %v each", name,
		first, buf.Len(), time.Since(start)/n)
}

func TestDial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s := NewConn(c)
		m, _ := s.Recv()
		s.Send(m)
	}()
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Send(Hello{Version: Version, Kind: "cli"})
	m, err := c.Recv()
	if err != nil || m != (Hello{Version: Version, Kind: "cli"}) {
		t.Fatalf("got %v, %v", m, err)
	}
}

func TestSocketPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if got, err := SocketPath(); err != nil || got != filepath.Join(dir, "pitwall", "pitwall.sock") {
		t.Fatalf("SocketPath() = %q, %v", got, err)
	}
	info, err := os.Lstat(filepath.Join(dir, "pitwall"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory permissions: %v, %v", info, err)
	}
}

func TestSocketPathRejectsUnsafeDirectory(t *testing.T) {
	for _, kind := range []string{"group read", "group write", "group execute", "other read", "other write", "other execute", "symlink", "file", "foreign owner"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_RUNTIME_DIR", root)
			dir := filepath.Join(root, "pitwall")
			switch kind {
			case "symlink":
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(dir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "foreign owner" {
					if err := os.Chown(dir, os.Getuid()+1, -1); err != nil {
						t.Skipf("cannot create a foreign-owned directory: %v", err)
					}
				} else {
					bits := map[string]os.FileMode{"group read": 0o040, "group write": 0o020, "group execute": 0o010, "other read": 0o004, "other write": 0o002, "other execute": 0o001}
					if err := os.Chmod(dir, 0o700|bits[kind]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if path, err := SocketPath(); err == nil || path != "" {
				t.Fatalf("SocketPath() = %q, %v; want an error and no path", path, err)
			}
		})
	}
}
