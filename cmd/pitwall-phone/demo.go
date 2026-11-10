package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// demo is the phone page's API over demo tabs: the real remote.Server,
// with tabs, a transcript and answers made up here, for screenshots and
// the emulator. Nothing in it is anyone's real work.
type demo struct {
	mu      sync.Mutex
	dir     string
	tabs    []remote.Tab
	changed chan struct{}
	next    uint64
}

// serveDemo serves demo variant (0 or 1, two machines' worth) at addr and
// returns its server and a pairing code good for 10 minutes.
func serveDemo(addr string, variant int) (url, code string, err error) {
	dir, err := os.MkdirTemp("", "pitwall-phone-demo-")
	if err != nil {
		return "", "", err
	}
	d := &demo{dir: dir, changed: make(chan struct{})}
	if err := d.fill(variant); err != nil {
		return "", "", err
	}
	s := &remote.Server{Dir: dir,
		Items:   func() []remote.Item { return nil },
		Tabs:    d.list,
		Changed: d.wait,
		Send:    d.send,
		Command: d.command,
		Answer:  d.answer,
		Reply:   func(pane string, _ int64, text string) error { _, err := d.send(pane, text); return err },
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", "", err
	}
	go http.Serve(ln, s.Handler())
	code, err = remote.Pair(dir, "demo phone", time.Now())
	return "http://" + ln.Addr().String() + "/", code, err
}

func (d *demo) list() []remote.Tab {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.tabs)
}

func (d *demo) wait() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.changed
}

// touch tells every stream the list changed. Callers hold d.mu.
func (d *demo) touch() {
	close(d.changed)
	d.changed = make(chan struct{})
}

func (d *demo) tab(pane string) (*remote.Tab, error) {
	i := slices.IndexFunc(d.tabs, func(t remote.Tab) bool { return t.Pane == pane })
	if i < 0 {
		return nil, fmt.Errorf("no running pane %s", pane)
	}
	return &d.tabs[i], nil
}

func busy(t *remote.Tab) bool {
	return t.State == string(model.StateWorking) || t.State == string(model.StatePendingApproval) || t.State == string(model.StateAwaitingInput)
}

// send queues text behind a turn, as the daemon does, or puts it in the
// transcript at once.
func (d *demo) send(pane, text string) (uint64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, err := d.tab(pane)
	if err != nil {
		return 0, err
	}
	if !t.Send {
		return 0, errors.New("the pane runs no agent pitwall knows")
	}
	d.next++
	if busy(t) {
		t.Queued++
	} else if t.Transcript != "" {
		appendLines(t.Transcript, userLine(text))
		t.State, t.Label, t.Detail = string(model.StateWorking), "Working", ""
	}
	d.touch()
	return d.next, nil
}

// command takes /model and /effort only between turns, as the daemon
// does, with its words.
func (d *demo) command(pane, command string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, err := d.tab(pane)
	if err != nil {
		return err
	}
	if busy(t) {
		return errors.New("claude is working on a turn; try again when it waits at its input box")
	}
	if name, ok := strings.CutPrefix(command, "/model "); ok && t.Transcript != "" {
		appendLines(t.Transcript, assistantLine(demoModel(name), "Set model to "+name+"."))
	}
	return nil
}

func (d *demo) answer(pane string, at int64, allow bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, err := d.tab(pane)
	if err != nil {
		return err
	}
	if t.At != at || !t.Answer {
		return errors.New("that prompt is gone")
	}
	t.Answer, t.Detail, t.At = false, "", time.Now().UnixNano()
	t.State, t.Label = string(model.StateWorking), "Working"
	if !allow {
		t.State, t.Label = string(model.StateCompleted), "Done"
	}
	d.touch()
	return nil
}

// demoModel is the id a Claude Code turn reports for a /model name.
func demoModel(name string) string {
	switch name {
	case "opus[1m]":
		return "claude-opus-5-5[1m]"
	case "default", "opus":
		return "claude-opus-5-5"
	case "fable":
		return "claude-fable-5-1"
	}
	return "claude-" + name + "-5-5"
}

func (d *demo) fill(variant int) error {
	now := time.Now()
	ago := func(m time.Duration) int64 { return now.Add(-m * time.Minute).UnixNano() }
	race, readme := filepath.Join(d.dir, "race.jsonl"), filepath.Join(d.dir, "readme.jsonl")
	if err := os.WriteFile(race, []byte(strings.Join(raceTranscript, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(readme, []byte(strings.Join(readmeTranscript, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	tab := func(id, session, group, name, agent, state, label, branch string, at int64) remote.Tab {
		t := remote.Tab{ID: id, Group: group, Branch: branch}
		t.Pane, t.Tab, t.Session, t.Agent, t.State, t.Label, t.At = "p-"+id, name, session, agent, state, label, at
		t.Provider = model.Provider(agent)
		t.Send = agent != "terminal"
		t.Picker = agent == "claude"
		return t
	}
	rate := tab("rate", "demo", "api", "Add rate limits", "codex", "pending-approval", "Approval", "feat/rate-limit", ago(1))
	rate.Detail, rate.Answer = "go test ./internal/limiter/...", true
	pr := tab("pr", "demo", "api", "Review the PR", "pi", "awaiting-input", "Input", "review/1.4", ago(2))
	pr.Detail, pr.Reply = "Should the review cover the docs changes too?", true
	fix := tab("fix", "demo", "", "Fix the checkout race", "claude", "working", "Working", "fix/checkout-race", ago(0))
	fix.Transcript = race
	doc := tab("doc", "demo", "", "Update the README", "claude", "completed", "Done", "docs/install", ago(12))
	doc.Transcript = readme
	site := tab("site", "demo", "", "Port the docs site", "claude", "parked", "Parked", "docs/site", ago(60))
	logs := tab("logs", "demo", "", "server logs", "terminal", "", "", "main", ago(180))
	certs := tab("certs", "infra", "", "Rotate the certs", "claude", "error", "Error", "ops/certs", ago(4))
	certs.Detail = "x509: certificate has expired"
	d.tabs = []remote.Tab{rate, pr, fix, doc, site, logs, certs}
	if variant == 1 {
		deps := tab("deps", "main", "", "Bump the deps", "codex", "working", "Working", "chore/deps", ago(0))
		notes := tab("notes", "main", "", "Write the release notes", "claude", "awaiting-input", "Input", "docs/release", ago(3))
		notes.Detail, notes.Reply = "Should the notes name the contributors?", true
		d.tabs = []remote.Tab{deps, notes}
	}
	// why: every daemon's ids are its own random ones; two demo servers in one app must not share any.
	tag := hex.EncodeToString(binary.BigEndian.AppendUint64(nil, rand.Uint64()))[:6]
	for i := range d.tabs {
		d.tabs[i].ID, d.tabs[i].Pane = tag+d.tabs[i].ID, tag+d.tabs[i].Pane
	}
	return nil
}

func appendLines(path string, lines ...string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(strings.Join(lines, "\n") + "\n")
}

func jsonLine(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func userLine(text string) string {
	return jsonLine(map[string]any{"type": "user", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]any{"role": "user", "content": text}})
}

func assistantLine(modelID string, blocks ...any) string {
	content := make([]any, len(blocks))
	for i, b := range blocks {
		if s, ok := b.(string); ok {
			b = map[string]any{"type": "text", "text": s}
		}
		content[i] = b
	}
	return jsonLine(map[string]any{"type": "assistant", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]any{"role": "assistant", "model": modelID, "content": content}})
}

func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(id, out string, failed bool) string {
	return jsonLine(map[string]any{"type": "user", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": failed}}}})
}

const raceOut = `WARNING: DATA RACE
Write by goroutine 31:
  checkout.(*Cart).Add()
      cart.go:42 +0x64
Previous read by goroutine 30:
  checkout.(*Cart).Total()
      cart.go:57 +0x3c
FAIL  example.com/shop/internal/checkout`

const opus = "claude-opus-5-5"

var raceTranscript = []string{
	userLine("The checkout total is off under load. Find the race and fix it."),
	assistantLine(opus, "I'll run the test under the race detector first, then read the cart code it points at.",
		toolUse("t1", "Bash", map[string]any{"command": "go test -race -count=20 ./internal/checkout/"})),
	toolResult("t1", raceOut, true),
	assistantLine(opus, toolUse("t2", "Read", map[string]any{"file_path": "internal/checkout/cart.go"})),
	toolResult("t2", "package checkout\n\ntype Cart struct {\n\titems []Item\n}", false),
	assistantLine(opus, "Found it. `Add` appends to `c.items` while `Total` ranges over it, and neither takes a lock. The fix:\n\n1. Give `Cart` a `sync.Mutex`.\n2. Hold it in `Add`, `Remove` and `Total`.\n\n```go\nfunc (c *Cart) Total() int {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\t...\n}\n```",
		toolUse("t3", "Edit", map[string]any{"file_path": "internal/checkout/cart.go", "old_string": "type Cart struct {", "new_string": "type Cart struct {\n\tmu sync.Mutex"})),
	toolResult("t3", "The file internal/checkout/cart.go has been updated.", false),
	assistantLine(opus, toolUse("t4", "Bash", map[string]any{"command": "go test -race -count=50 ./internal/checkout/"})),
}

var readmeTranscript = []string{
	userLine("Update the README's install steps for the new installer."),
	assistantLine(opus, "Done. The README now starts with `curl -fsSL https://example.com/get.sh | sh`, and the old manual steps moved under **Build from source**."),
}
