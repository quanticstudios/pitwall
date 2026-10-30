package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type historyPane struct {
	*fakePane
	term vt.Emulator
}

func (p *historyPane) SearchContext(ctx context.Context, q string, limit int) ([]vt.SearchLine, bool) {
	return p.term.(interface {
		SearchContext(context.Context, string, int) ([]vt.SearchLine, bool)
	}).SearchContext(ctx, q, limit)
}

func (p *historyPane) Search(q string, limit int) ([]vt.Match, bool) { return p.term.Search(q, limit) }
func (p *historyPane) Text(s vt.Selection) string                    { return p.term.Text(s) }

func searchFixture(t testing.TB, tabs, lines int) *Daemon {
	t.Helper()
	d := &Daemon{panes: map[string]Pane{}}
	text := strings.Repeat("terminal output with a realistic line of build progress for search benchmarks\r\n", lines/2)
	text += "unique needle in history\r\n"
	text += strings.Repeat("more build progress in retained scrollback for every session being searched\r\n", lines-lines/2+24)
	for i := range tabs {
		id, ws := fmt.Sprintf("p%d", i), fmt.Sprintf("w%d", i)
		e := vt.New(80, 24, io.Discard)
		e.(interface{ SetScrollback(int) }).SetScrollback(lines)
		if _, err := e.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
		if got := e.ScrollbackLen(); got != lines {
			t.Fatalf("fixture retained %d history lines, want %d", got, lines)
		}
		d.panes[id] = &historyPane{fakePane: &fakePane{}, term: e}
		d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: ws})
		d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: ws, Detached: i%2 == 1})
	}
	return d
}

func TestSearchAllScrollbackAndTranscripts(t *testing.T) {
	d := searchFixture(t, 3, 100)
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","timestamp":"2026-10-30T12:00:00Z","message":{"content":"private needle from a transcript"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.st.Panes[1].Provider, d.st.Panes[1].Transcript = model.ProviderClaude, path
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	var replies []proto.SearchAllResult
	d.searchAll(context.Background(), proto.SearchAll{ID: 7, Query: "needle"}, func(r proto.SearchAllResult) { replies = append(replies, r) })
	if len(replies) != 5 || !replies[4].Done {
		t.Fatalf("stream %+v", replies)
	}
	for i := range 3 {
		r := replies[i]
		if r.ID != 7 || r.Done || len(r.Matches) != 1 || r.Matches[0].Workspace != fmt.Sprintf("w%d", i) || !strings.Contains(r.Matches[0].Snippet, "needle") || r.Matches[0].Time.IsZero() {
			t.Fatalf("scrollback batch %+v", r)
		}
	}
	m := replies[3].Matches[0]
	if !m.Transcript || m.Pane != "p1" || m.Match.Line != 1 || m.Time.IsZero() {
		t.Fatalf("transcript match %+v", m)
	}
	if logs.Len() != 0 {
		t.Fatalf("search wrote to logs: %q", logs.String())
	}
	d.panes["p0"].(*historyPane).term.Write([]byte("\x1b[?1049hfullscreen app"))
	hidden := false
	d.searchAll(context.Background(), proto.SearchAll{Query: "needle"}, func(r proto.SearchAllResult) {
		for _, m := range r.Matches {
			if m.Pane == "p0" && m.Hidden {
				hidden = true
			}
		}
	})
	if !hidden {
		t.Fatal("retained scrollback behind fullscreen app was not found")
	}
}

func TestSearchAllCancellationAndLimit(t *testing.T) {
	d := searchFixture(t, 2, 200)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	d.searchAll(ctx, proto.SearchAll{Query: "needle"}, func(r proto.SearchAllResult) { n++; cancel() })
	if n != 1 {
		t.Fatalf("got %d batches after cancelling the first", n)
	}
	var matches int
	var done proto.SearchAllResult
	d.searchAll(context.Background(), proto.SearchAll{Query: "progress"}, func(r proto.SearchAllResult) { matches += len(r.Matches); done = r })
	if matches != 2*searchPerPane || !done.Done || !done.More {
		t.Fatalf("limit: %d matches, final %+v", matches, done)
	}
	for _, q := range []string{"", strings.Repeat("x", 1025), "absent"} {
		n = 0
		d.searchAll(context.Background(), proto.SearchAll{Query: q}, func(r proto.SearchAllResult) {
			n++
			if !r.Done || len(r.Matches) != 0 {
				t.Errorf("unexpected result %+v", r)
			}
		})
		if n != 1 {
			t.Errorf("empty result sent %d batches", n)
		}
	}
}

// BenchmarkSearchAll30Tabs10000Lines exercises the full daemon path, including
// snippets. Fixture construction is outside timing. ENG-2715's target is
// 300 ms/op, including a no-match query that must scan every retained line.
func BenchmarkSearchAll30Tabs10000Lines(b *testing.B) {
	d := searchFixture(b, 30, 10000)
	for _, query := range []string{"needle", "absent", "progress"} {
		b.Run(query, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				d.searchAll(context.Background(), proto.SearchAll{Query: query}, func(proto.SearchAllResult) {})
			}
		})
	}
}

type blockedSearchPane struct {
	*fakePane
	started chan struct{}
	release chan struct{}
}

func (p *blockedSearchPane) Search(q string, limit int) ([]vt.Match, bool) {
	if q == "first" {
		close(p.started)
		<-p.release
	}
	return []vt.Match{{Line: 1}}, false
}

func TestSearchAllConnectionReplacement(t *testing.T) {
	p := &blockedSearchPane{fakePane: &fakePane{}, started: make(chan struct{}), release: make(chan struct{})}
	d := &Daemon{panes: map[string]Pane{"p": p}, st: model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w"}}}}
	server, client := net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan struct{})
	go func() { d.serveConn(context.Background(), server); close(done) }()
	c := proto.NewConn(client)
	send := func(m any) {
		t.Helper()
		if err := c.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	send(proto.Hello{Kind: "cli", Version: proto.Version, Level: proto.Level})
	send(proto.SearchAll{ID: 1, Query: "first"})
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first query never started")
	}
	// The reader must accept and complete a replacement while the old search
	// is blocked. A synchronous handler would time out on this second send.
	send(proto.SearchAll{ID: 2, Query: "second"})
	for {
		m, err := c.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if r, ok := m.(proto.SearchAllResult); ok && r.ID == 2 && r.Done {
			break
		}
	}
	close(p.release)
	send(proto.SearchAll{ID: 3}) // explicit cancellation is accepted too
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection did not exit")
	}
}
