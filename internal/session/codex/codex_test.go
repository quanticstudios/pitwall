package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

var update = flag.Bool("update", false, "rewrite testdata/*.events.json from the driver's output")

func TestMain(m *testing.M) {
	if script := os.Getenv("PITWALL_CODEX_FAKE"); script != "" {
		os.Exit(fakeServer(script))
	}
	testenv.Main(m)
}

// fakeServer plays a transcript as `codex app-server` would. Each line is
// one step: {"in": msg} reads a line and checks it is msg ("$CWD" stands
// for PITWALL_CODEX_FAKE_CWD), {"out": msg} writes msg, {"await": name}
// waits for that file in the runs folder, {"exit": code, "stderr": text}
// ends the process. Exit lines split the transcript into sections; the
// nth process started plays the nth, counted by run-* files in
// PITWALL_CODEX_FAKE_RUNS. A section without an exit runs until stdin ends.
func fakeServer(script string) int {
	runs := os.Getenv("PITWALL_CODEX_FAKE_RUNS")
	started, _ := filepath.Glob(filepath.Join(runs, "run-*"))
	run := len(started)
	os.WriteFile(filepath.Join(runs, fmt.Sprintf("run-%d", run)), nil, 0o600)
	b, err := os.ReadFile(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cwd, _ := json.Marshal(os.Getenv("PITWALL_CODEX_FAKE_CWD"))
	b = bytes.ReplaceAll(b, []byte("$CWD"), cwd[1:len(cwd)-1])
	in := bufio.NewReader(os.Stdin)
	section := 0
	for line := range bytes.Lines(b) {
		var step struct {
			In, Out json.RawMessage
			Await   string
			Exit    *int
			Stderr  string
		}
		if err := json.Unmarshal(line, &step); err != nil {
			fmt.Fprintln(os.Stderr, "bad step:", err)
			return 2
		}
		if section < run {
			if step.Exit != nil {
				section++
			}
			continue
		}
		switch {
		case step.In != nil:
			got, _ := in.ReadBytes('\n')
			var g, w any
			if json.Unmarshal(got, &g) != nil || json.Unmarshal(step.In, &w) != nil || !reflect.DeepEqual(g, w) {
				fmt.Fprintf(os.Stderr, "got %s want %s\n", bytes.TrimSpace(got), step.In)
				return 3
			}
		case step.Out != nil:
			var out bytes.Buffer
			json.Compact(&out, step.Out)
			os.Stdout.Write(append(out.Bytes(), '\n'))
		case step.Await != "":
			for {
				if _, err := os.Stat(filepath.Join(runs, step.Await)); err == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		case step.Exit != nil:
			fmt.Fprint(os.Stderr, step.Stderr)
			return *step.Exit
		}
	}
	io.Copy(io.Discard, in)
	return 0
}

// recorder keeps what a driver emits.
type recorder struct {
	mu     sync.Mutex
	events []session.Event
}

func (r *recorder) emit(e session.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []session.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]session.Event(nil), r.events...)
}

func (r *recorder) waitFor(t *testing.T, typ session.EventType) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for _, e := range r.all() {
			if e.Type == typ {
				return
			}
		}
	}
	t.Fatalf("no %s event in %v", typ, r.all())
}

// start runs the driver against the fake server playing testdata/name.
func start(t *testing.T, name string, cfg session.Config) (session.Driver, *recorder, string) {
	t.Helper()
	now = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	script, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	t.Setenv("PITWALL_CODEX_COMMAND", os.Args[0])
	cfg.ID, cfg.Provider, cfg.Cwd = "c1", model.ProviderCodex, t.TempDir()
	// The fake's settings ride in Config.Env, which only the process sees.
	cfg.Env = []string{"PITWALL_CODEX_FAKE=" + script, "PITWALL_CODEX_FAKE_RUNS=" + runs, "PITWALL_CODEX_FAKE_CWD=" + cfg.Cwd}
	rec := &recorder{}
	d, err := Provider{}.Start(context.Background(), cfg, session.Origin{}, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	return d, rec, runs
}

// golden compares events with testdata/name, or rewrites it with -update.
func golden(t *testing.T, name string, events []session.Event) {
	t.Helper()
	got, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("events differ from %s (go test -update rewrites it):\n%s", path, got)
	}
}

// fold reduces events into a chat snapshot, as the chat loop will.
func fold(t *testing.T, events []session.Event) session.Snapshot {
	t.Helper()
	s := session.Snapshot{SessionID: "c1", Provider: model.ProviderCodex, SessionState: session.StateReady}
	var seq int64
	for _, e := range events {
		for _, d := range session.Reduce(s, e) {
			seq++
			d.Version, d.SessionID, d.Seq = session.DeltaVersion, "c1", seq
			n, err := session.Apply(&s, seq-1, d)
			if err != nil {
				t.Fatalf("%s: %v", e.Type, err)
			}
			s = n
		}
	}
	return s
}

func TestTurn(t *testing.T) {
	d, rec, runs := start(t, "turn.jsonl", session.Config{Model: "gpt-6.1-sol", Effort: "high"})
	ctx := context.Background()
	id, err := d.Send(ctx, session.Message{ID: "u1", Text: "List the files", Images: []session.Image{{MimeType: "image/png", Data: []byte("png")}}})
	if err != nil || id != "turn_1" {
		t.Fatalf("Send = %q, %v; want turn_1", id, err)
	}
	if _, err := d.Send(ctx, session.Message{Text: "again"}); !errors.Is(err, session.ErrBusy) {
		t.Errorf("Send during a turn = %v, want ErrBusy", err)
	}
	os.WriteFile(filepath.Join(runs, "go"), nil, 0o600)
	rec.waitFor(t, session.EventTurnCompleted)
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	events := rec.all()
	golden(t, "turn.events.json", events)

	s := fold(t, events)
	if len(s.Turns) != 1 || s.Turns[0].State != session.TurnCompleted {
		t.Errorf("turns = %+v, want turn_1 completed", s.Turns)
	}
	var kinds []string
	for _, it := range s.Content {
		kinds = append(kinds, string(it.Kind)+":"+it.Phase)
	}
	if want := "reasoning-summary-text: assistant-text:commentary tool-call: tool-call: assistant-text:final_answer"; strings.Join(kinds, " ") != want {
		t.Errorf("content = %s, want %s", strings.Join(kinds, " "), want)
	}
	if s.Usage == nil || s.Usage.InputTokens != 1200 {
		t.Errorf("usage = %+v, want 1200 input tokens", s.Usage)
	}
}

func TestCrashRestarts(t *testing.T) {
	d, rec, runs := start(t, "crash.jsonl", session.Config{Mode: "bypassPermissions"})
	ctx := context.Background()
	if id, err := d.Send(ctx, session.Message{ID: "u1", Text: "Work"}); err != nil || id != "turn_1" {
		t.Fatalf("Send = %q, %v; want turn_1", id, err)
	}
	rec.waitFor(t, session.EventExited)
	crashed := fold(t, rec.all())
	if crashed.SessionState != session.StateError || crashed.Turns[0].State != session.TurnFailed {
		t.Errorf("after the crash: state %s, turn %+v; want error and a failed turn", crashed.SessionState, crashed.Turns[0])
	}
	if crashed.LastError == nil || !strings.Contains(*crashed.LastError, "panicked: boom") {
		t.Errorf("lastError = %v, want the stderr tail", crashed.LastError)
	}

	// The next send starts a server that resumes the thread.
	if id, err := d.Send(ctx, session.Message{ID: "u2", Text: "Again"}); err != nil || id != "turn_2" {
		t.Fatalf("Send after the crash = %q, %v; want turn_2", id, err)
	}
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if started, _ := filepath.Glob(filepath.Join(runs, "run-*")); len(started) != 2 {
		t.Errorf("%d servers started, want 2", len(started))
	}
	golden(t, "crash.events.json", rec.all())
}

func TestCrashLoopRefused(t *testing.T) {
	d, rec, runs := start(t, "crashloop.jsonl", session.Config{})
	defer d.Close(context.Background())
	rec.waitFor(t, session.EventExited)
	for i := range maxRestarts {
		if _, err := d.Send(context.Background(), session.Message{Text: "x"}); !errors.Is(err, session.ErrClosed) {
			t.Fatalf("send %d = %v, want ErrClosed from a server that dies at once", i, err)
		}
	}
	var pe *session.ProviderError
	if _, err := d.Send(context.Background(), session.Message{Text: "x"}); !errors.As(err, &pe) || pe.Op != "restart" {
		t.Fatalf("send after %d crashes = %v, want a refused restart", maxRestarts, err)
	}
	if started, _ := filepath.Glob(filepath.Join(runs, "run-*")); len(started) != 1+maxRestarts {
		t.Errorf("%d servers started, want %d", len(started), 1+maxRestarts)
	}
}
