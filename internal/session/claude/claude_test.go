package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

var update = flag.Bool("update", false, "rewrite testdata/*.events.jsonl from what the driver emits")

// testdata holds the spike's captures (ENG-2677): <name>.args, the command
// line; <name>.stdin.jsonl, what the probe wrote; <name>.stdout.jsonl,
// what Claude Code 2.1.314 printed. <name>.events.jsonl is what the driver
// emits when the fake replays it, with createdAt blank and the session id
// as SESSION.

// TestMain makes the test binary a fake claude when FAKE_CLAUDE is set,
// so no test starts a real one.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CLAUDE") != "" {
		fakeClaude()
		return
	}
	testenv.Main(m)
}

// fakeClaude replays FAKE_CLAUDE (a stdout capture): each user line on
// stdin gets the capture's next lines, up to and including a result. It
// logs its arguments and stdin to FAKE_CLAUDE_LOG. FAKE_CLAUDE_MODE crash
// exits 3 on the first user line; hang prints nothing and outlives stdin.
func fakeClaude() {
	logf, err := os.Create(os.Getenv("FAKE_CLAUDE_LOG"))
	if err != nil {
		os.Exit(2)
	}
	defer logf.Close()
	args, _ := json.Marshal(os.Args[1:])
	fmt.Fprintf(logf, "%s\n", args)

	capture, err := os.ReadFile(os.Getenv("FAKE_CLAUDE"))
	if err != nil {
		os.Exit(2)
	}
	out := bytes.SplitAfter(capture, []byte("\n"))
	// A new session prints the id it was given, as the real CLI does.
	if i := slices.Index(os.Args, "--session-id"); i > 0 {
		var first struct {
			SessionID string `json:"session_id"`
		}
		for _, l := range out {
			if json.Unmarshal(l, &first) == nil && first.SessionID != "" {
				break
			}
		}
		for j := range out {
			out[j] = bytes.ReplaceAll(out[j], []byte(first.SessionID), []byte(os.Args[i+1]))
		}
	}

	mode := os.Getenv("FAKE_CLAUDE_MODE")
	in := bufio.NewReader(os.Stdin)
	for {
		l, err := in.ReadBytes('\n')
		if len(l) > 0 {
			logf.Write(l)
			var msg struct{ Type string }
			json.Unmarshal(l, &msg)
			switch {
			case msg.Type != "user" || mode == "hang":
			case mode == "crash":
				fmt.Fprint(os.Stderr, "boom")
				os.Exit(3)
			default:
				for len(out) > 0 {
					o := out[0]
					out = out[1:]
					os.Stdout.Write(o)
					if json.Unmarshal(o, &msg) == nil && msg.Type == "result" {
						break
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// run is one driver over the fake replaying testdata/<name>, and what it
// emitted.
type run struct {
	t      *testing.T
	d      session.Driver
	log    string
	mu     sync.Mutex
	events []session.Event
	ch     chan session.Event
}

func start(t *testing.T, name, mode string, from session.Origin) *run {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	capture, err := filepath.Abs(filepath.Join("testdata", name+".stdout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r := &run{t: t, log: filepath.Join(t.TempDir(), "fake.log"), ch: make(chan session.Event, 10000)}
	if _, set := os.LookupEnv(CommandEnv); !set {
		t.Setenv(CommandEnv, self)
	}
	t.Setenv("FAKE_CLAUDE", capture)
	t.Setenv("FAKE_CLAUDE_LOG", r.log)
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	t.Setenv("GORACE", "atexit_sleep_ms=0") // a -race fake would sleep 1 s on exit
	cfg := session.Config{Cwd: t.TempDir(), Model: "claude-sonnet-5-5", Effort: "low"}
	r.d, err = Provider{}.Start(context.Background(), cfg, from, func(e session.Event) {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
		r.ch <- e
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.close(5 * time.Second) })
	return r
}

// waitFor returns the first event, from now on, that ok accepts.
func (r *run) waitFor(what string, ok func(session.Event) bool) session.Event {
	r.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-r.ch:
			if ok(e) {
				return e
			}
		case <-timeout:
			r.t.Fatalf("no %s", what)
		}
	}
}

// turn sends m and waits for its turn to end.
func (r *run) turn(m session.Message) session.Event {
	r.t.Helper()
	id, err := r.d.Send(context.Background(), m)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.waitFor("end of "+id, func(e session.Event) bool {
		return e.Type == session.EventTurnCompleted && e.TurnID == id
	})
}

func (r *run) close(d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	r.d.Close(ctx)
}

func (r *run) emitted() []session.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// stdin is what the driver wrote, one decoded line each, after the
// fake's argument line.
func (r *run) stdin() (args []string, lines []map[string]any) {
	r.t.Helper()
	b, err := os.ReadFile(r.log)
	if err != nil {
		r.t.Fatal(err)
	}
	all := strings.Split(strings.TrimSpace(string(b)), "\n")
	json.Unmarshal([]byte(all[0]), &args)
	for _, l := range all[1:] {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			r.t.Fatalf("%s: %v", l, err)
		}
		lines = append(lines, v)
	}
	return args, lines
}

// prompts are the user messages in testdata/<name>.stdin.jsonl, the
// spike's turns.
func prompts(t *testing.T, name string) (out []session.Message, lines []map[string]any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".stdin.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var in struct {
			Type    string
			Message struct{ Content json.RawMessage }
		}
		json.Unmarshal([]byte(l), &in)
		if in.Type != "user" {
			continue
		}
		var v map[string]any
		json.Unmarshal([]byte(l), &v)
		lines = append(lines, v)
		m := session.Message{ID: fmt.Sprintf("turn-%d", len(out)+1)}
		if json.Unmarshal(in.Message.Content, &m.Text) != nil {
			var blocks []struct {
				Type, Text string
				Source     struct {
					MediaType string `json:"media_type"`
					Data      string
				}
			}
			json.Unmarshal(in.Message.Content, &blocks)
			for _, bl := range blocks {
				if bl.Type == "text" {
					m.Text = bl.Text
				} else {
					data, _ := base64.StdEncoding.DecodeString(bl.Source.Data)
					m.Images = append(m.Images, session.Image{MimeType: bl.Source.MediaType, Data: data})
				}
			}
		}
		out = append(out, m)
	}
	return out, lines
}

// TestFixtures replays every capture, one turn per prompt the spike sent,
// and checks the events against testdata/<name>.events.jsonl.
func TestFixtures(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.stdout.jsonl")
	if len(paths) == 0 {
		t.Fatal("no captures")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".stdout.jsonl")
		t.Run(name, func(t *testing.T) {
			r := start(t, name, "", session.Origin{})
			msgs, _ := prompts(t, name)
			for _, m := range msgs {
				r.turn(m)
			}
			r.close(5 * time.Second)
			events := r.emitted()
			id := events[1].ResumeID // Start's own session.resume-id
			var got bytes.Buffer
			for _, e := range events {
				e.CreatedAt = ""
				b, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				got.Write(bytes.ReplaceAll(b, []byte(id), []byte("SESSION")))
				got.WriteByte('\n')
			}
			golden := filepath.Join("testdata", name+".events.jsonl")
			if *update {
				if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Errorf("events differ from %s (go test -run TestFixtures -update to rewrite it):\n%s", golden, got.Bytes())
			}
		})
	}
}

// TestMultiTurn: two turns share one process and one session id, the
// one the driver chose and passed as --session-id.
func TestMultiTurn(t *testing.T) {
	r := start(t, "resume-1-start", "", session.Origin{})
	msgs, _ := prompts(t, "resume-1-start")
	if len(msgs) != 2 {
		t.Fatalf("prompts = %d", len(msgs))
	}
	first, second := r.turn(msgs[0]), r.turn(msgs[1])
	if first.State != session.TurnCompleted || second.State != session.TurnCompleted {
		t.Errorf("turn states = %q, %q", first.State, second.State)
	}
	// The spike resumed at the first turn's last assistant message.
	if first.Point != "07a45f3e-76ca-4492-9149-41725037db79" {
		t.Errorf("first point = %q", first.Point)
	}
	r.close(5 * time.Second)

	args, _ := r.stdin()
	i := slices.Index(args, "--session-id")
	if i < 0 {
		t.Fatalf("args = %q", args)
	}
	var ids []string
	exits := 0
	for _, e := range r.emitted() {
		switch e.Type {
		case session.EventResumeID:
			ids = append(ids, e.ResumeID)
		case session.EventExited:
			exits++
		}
	}
	for _, id := range ids {
		if id != args[i+1] {
			t.Errorf("resume ids = %q, want all %q", ids, args[i+1])
		}
	}
	if exits != 1 {
		t.Errorf("session.exited %d times", exits)
	}
}

// TestFork: a fork reports the CLI's new session id with its version and
// capabilities, from system/init.
func TestFork(t *testing.T) {
	r := start(t, "resume-3-fork", "", session.Origin{Resume: "40ab806d-5ff7-459a-926e-a263e6c244e4", Fork: true})
	msgs, _ := prompts(t, "resume-3-fork")
	r.turn(msgs[0])
	r.close(5 * time.Second)
	var ids []session.Event
	for _, e := range r.emitted() {
		if e.Type == session.EventResumeID {
			ids = append(ids, e)
		}
	}
	if len(ids) != 1 || ids[0].ResumeID == "40ab806d-5ff7-459a-926e-a263e6c244e4" || ids[0].Version != "2.1.314" || len(ids[0].Capabilities) == 0 {
		t.Errorf("resume-id events = %+v", ids)
	}
	args, _ := r.stdin()
	if !slices.Contains(args, "--fork-session") || slices.Contains(args, "--session-id") {
		t.Errorf("args = %q", args)
	}
}

func TestArgs(t *testing.T) {
	base := "-p --input-format stream-json --output-format stream-json --verbose --include-partial-messages "
	for _, c := range []struct {
		cfg  session.Config
		from session.Origin
		want string
	}{
		{session.Config{}, session.Origin{}, "--session-id ID"},
		{session.Config{Model: "claude-opus-5-5", Effort: "high", Mode: "plan"}, session.Origin{},
			"--session-id ID --model claude-opus-5-5 --effort high --permission-mode plan"},
		{session.Config{}, session.Origin{Resume: "R"}, "--resume R"},
		{session.Config{}, session.Origin{Resume: "R", At: "U", Fork: true}, "--resume R --resume-session-at U --fork-session"},
	} {
		if got := strings.Join(args(c.cfg, c.from, "ID"), " "); got != base+c.want {
			t.Errorf("args(%+v, %+v) = %q", c.cfg, c.from, got)
		}
	}
}

// TestStdin: the user line is the spike's, plus the uuid that names the
// turn; a control request the driver does not handle gets an error.
func TestStdin(t *testing.T) {
	r := start(t, "image", "", session.Origin{})
	msgs, want := prompts(t, "image")
	r.turn(msgs[0])
	r.close(5 * time.Second)
	_, lines := r.stdin()
	want[0]["uuid"] = "turn-1"
	if !reflect.DeepEqual(lines[0], want[0]) {
		t.Errorf("user line = %v\nwant %v", lines[0], want[0])
	}

	r = start(t, "hooks", "", session.Origin{})
	msgs, _ = prompts(t, "hooks")
	r.turn(msgs[0])
	r.close(5 * time.Second)
	_, lines = r.stdin()
	b, _ := json.Marshal(lines)
	if !strings.Contains(string(b), `"error":"pitwall does not handle hook_callback"`) {
		t.Errorf("no error answer to hook_callback in %s", b)
	}
}

// TestTurnTiming: a tool call's item starts at its tool_use and ends at
// its result, and streamed text is not repeated by the finished line.
func TestTurnTiming(t *testing.T) {
	r := start(t, "interrupt-partial", "", session.Origin{})
	msgs, _ := prompts(t, "interrupt-partial")
	if end := r.turn(msgs[0]); end.State != session.TurnInterrupted {
		t.Errorf("interrupted turn state = %q", end.State)
	}
	r.close(5 * time.Second)
	text := ""
	for _, e := range r.emitted() {
		if e.Kind == session.KindAssistantText {
			text += e.Text
		}
	}
	if !strings.HasSuffix(text, "\n96") || strings.Count(text, "\n13\n") != 1 {
		t.Errorf("assistant text = %q", text)
	}

	r = start(t, "permission-mcp", "", session.Origin{})
	msgs, _ = prompts(t, "permission-mcp")
	r.turn(msgs[0])
	r.close(5 * time.Second)
	var calls []session.Event
	for _, e := range r.emitted() {
		if e.Tool != nil && e.Tool.ToolName == "Write" {
			calls = append(calls, e)
		}
	}
	if len(calls) != 2 || calls[0].Tool.Status != "running" || calls[1].Tool.Status != "completed" ||
		calls[0].CreatedAt == "" || calls[1].CreatedAt < calls[0].CreatedAt || !strings.HasPrefix(calls[1].Tool.Output, "File created") {
		t.Errorf("Write call events = %+v", calls)
	}
}

// TestCrash: a CLI that exits on its own fails the turn with its exit code
// and stderr, and the driver refuses sends after.
func TestCrash(t *testing.T) {
	r := start(t, "basic", "crash", session.Origin{})
	if _, err := r.d.Send(context.Background(), session.Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	e := r.waitFor("error", func(e session.Event) bool { return e.Type == session.EventError })
	if e.Message != "Claude exited (code 3)\nboom" {
		t.Errorf("error = %q", e.Message)
	}
	x := r.waitFor("exit", func(e session.Event) bool { return e.Type == session.EventExited })
	if x.ExitCode == nil || *x.ExitCode != 3 {
		t.Errorf("exit code = %v", x.ExitCode)
	}
	if _, err := r.d.Send(context.Background(), session.Message{Text: "again"}); !errors.Is(err, session.ErrClosed) {
		t.Errorf("send after exit: %v", err)
	}
}

// TestCloseKills: a CLI that ignores the closed stdin is killed when
// Close's context ends, and nothing is emitted after Close returns.
func TestCloseKills(t *testing.T) {
	r := start(t, "basic", "hang", session.Origin{})
	if _, err := r.d.Send(context.Background(), session.Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.d.Send(context.Background(), session.Message{Text: "too soon"}); !errors.Is(err, session.ErrBusy) {
		t.Errorf("second send: %v", err)
	}
	began := time.Now()
	r.close(200 * time.Millisecond)
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("Close took %v", d)
	}
	events := r.emitted()
	last := events[len(events)-1]
	if last.Type != session.EventExited || (runtime.GOOS != "windows" && last.ExitCode != nil) {
		t.Errorf("last event = %+v", last)
	}
	for _, e := range events {
		if e.Type == session.EventError {
			t.Errorf("a closed process reported %q", e.Message)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.emitted()); n != len(events) {
		t.Errorf("%d events after Close", n-len(events))
	}
}

// TestPath: with no PITWALL_CLAUDE_COMMAND the driver runs claude from
// PATH.
func TestPath(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := "claude"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, exe), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()

	t.Setenv(CommandEnv, "")
	t.Setenv("PATH", dir)
	r := start(t, "basic", "", session.Origin{})
	msgs, _ := prompts(t, "basic")
	if end := r.turn(msgs[0]); end.State != session.TurnCompleted {
		t.Errorf("turn = %+v", end)
	}
}
