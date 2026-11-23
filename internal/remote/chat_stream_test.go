package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
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
