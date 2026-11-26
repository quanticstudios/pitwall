package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// TestStreamWatchedChat: the stream of a live agent pane's chat takes its
// entries from ChatEntries, not the transcript, sends them redacted when
// Watch wakes it, reads them no more at its poll, and stops watching when
// the page goes.
func TestStreamWatchedChat(t *testing.T) {
	s, _ := newServer(t)
	s.Tabs = func() []Tab {
		return []Tab{{Item: Item{Pane: "p"}, Provider: model.ProviderClaude, Transcript: "/no/such/file.jsonl"}}
	}
	never := make(chan struct{})
	s.Changed = func() <-chan struct{} { return never }
	var mu sync.Mutex
	entries := []flow.Entry{{Seq: 1, Kind: flow.EntryUser, Text: "hello"}}
	var reads atomic.Int32
	s.ChatEntries = func(string) ([]flow.Entry, string, bool) {
		reads.Add(1)
		mu.Lock()
		defer mu.Unlock()
		return append([]flow.Entry(nil), entries...), "opus", true
	}
	wake := make(chan struct{}, 1)
	stopped := make(chan struct{})
	s.Watch = func(pane string) (<-chan struct{}, func()) {
		return wake, func() { close(stopped) }
	}
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/stream?pane=p", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(resp.Body)
	event := func(want string) map[string]any {
		t.Helper()
		name := ""
		for scan.Scan() {
			line := scan.Text()
			if n, ok := strings.CutPrefix(line, "event: "); ok {
				name = n
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok && name == want {
				var m map[string]any
				json.Unmarshal([]byte(data), &m)
				return m
			}
		}
		t.Fatalf("stream ended before %s: %v", want, scan.Err())
		return nil
	}
	if m := event("reset"); len(m["entries"].([]any)) != 1 {
		t.Fatalf("reset %v", m)
	}
	n := reads.Load()
	time.Sleep(3 * pollEvery)
	if got := reads.Load() - n; got != 0 {
		t.Errorf("the poll read the watched chat %d times", got)
	}
	mu.Lock()
	entries = append(entries, flow.Entry{Seq: 2, Kind: flow.EntryAssistant, Text: "token=sk-abcdefghijklmnopqrstuvwxyz0123"})
	mu.Unlock()
	at := time.Now()
	wake <- struct{}{}
	m := event("entries")
	if took := time.Since(at); took >= pollEvery {
		t.Errorf("the entry took %v, as long as a poll", took)
	}
	if b, _ := json.Marshal(m); !strings.Contains(string(b), `"seq":2`) || strings.Contains(string(b), "sk-abcdef") {
		t.Fatalf("entries %s: want entry 2, redacted", b)
	}
	resp.Body.Close()
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not stop watching")
	}
}

// TestStreamCommands: the stream sends a chat's / menu as "commands" after
// its first entries, again only when the list changed, and an empty list
// as [] rather than null.
func TestStreamCommands(t *testing.T) {
	s, _ := newServer(t)
	s.Tabs = func() []Tab { return []Tab{{Item: Item{Pane: "p"}, Provider: model.ProviderClaude}} }
	never := make(chan struct{})
	s.Changed = func() <-chan struct{} { return never }
	var mu sync.Mutex
	entries := []flow.Entry{{Seq: 1, Kind: flow.EntryUser, Text: "hello"}}
	cmds := []session.Command{{Name: "review", Source: "user"}}
	s.ChatEntries = func(string) ([]flow.Entry, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(entries), "", true
	}
	s.ChatMenu = func(string) []session.Command {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(cmds)
	}
	wake := make(chan struct{}, 1)
	s.Watch = func(string) (<-chan struct{}, func()) { return wake, func() {} }
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/stream?pane=p", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	next := func() (string, string) {
		t.Helper()
		name := ""
		for scan.Scan() {
			line := scan.Text()
			if n, ok := strings.CutPrefix(line, "event: "); ok {
				name = n
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok && name != "hello" && name != "tabs" {
				return name, data
			}
		}
		t.Fatalf("stream ended: %v", scan.Err())
		return "", ""
	}
	expect := func(name, data string) {
		t.Helper()
		if n, d := next(); n != name || d != data {
			t.Fatalf("got %s %s, want %s %s", n, d, name, data)
		}
	}
	if n, _ := next(); n != "reset" {
		t.Fatalf("first chat event %s, want reset", n)
	}
	expect("commands", `{"commands":[{"name":"review","source":"user"}]}`)
	mu.Lock()
	entries = append(entries, flow.Entry{Seq: 2, Kind: flow.EntryAssistant, Text: "hi"})
	mu.Unlock()
	wake <- struct{}{}
	if n, _ := next(); n != "entries" {
		t.Fatalf("got %s, want entries", n)
	}
	mu.Lock()
	cmds = nil
	mu.Unlock()
	wake <- struct{}{}
	expect("commands", `{"commands":[]}`) // no second list for the unchanged one before it
}
