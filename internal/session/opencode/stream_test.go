package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

func TestStreamMappings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []string
		check   func(*testing.T, []session.Event)
	}{
		{"failed-tool", []string{
			`{"type":"message.updated","properties":{"info":{"sessionID":"ses_fake","id":"msg_a","role":"assistant"}}}`,
			`{"type":"message.part.updated","properties":{"part":{"sessionID":"ses_fake","messageID":"msg_a","id":"prt_tool","type":"tool","callID":"call","tool":"read","state":{"status":"pending"}}}}`,
			`{"type":"message.part.updated","properties":{"part":{"sessionID":"ses_fake","messageID":"msg_a","id":"prt_tool","type":"tool","callID":"call","tool":"read","state":{"status":"error","input":{"filePath":"a.go"},"error":"missing"}}}}`,
		}, func(t *testing.T, got []session.Event) {
			if len(got) != 2 || got[0].Tool.Status != "running" || string(got[0].Tool.Input) != `{}` || got[1].Tool.Status != "failed" || got[1].Tool.Output != "missing" {
				t.Fatal(got)
			}
		}},
		{"read-and-external-resolution", []string{
			`{"type":"permission.asked","properties":{"sessionID":"ses_fake","id":"per_read","permission":"read","patterns":["a.go"],"metadata":{"filePath":"a.go"}}}`,
			`{"type":"permission.asked","properties":{"sessionID":"ses_fake","id":"per_read","permission":"read"}}`,
			`{"type":"permission.replied","properties":{"sessionID":"ses_fake","requestID":"per_read","reply":"reject"}}`,
			`{"type":"question.asked","properties":{"sessionID":"ses_fake","id":"que","questions":[]}}`,
			`{"type":"question.rejected","properties":{"sessionID":"ses_fake","requestID":"que"}}`,
		}, func(t *testing.T, got []session.Event) {
			if len(got) != 4 || got[0].RequestType != session.RequestFileRead || got[0].Request.FilePath != "a.go" || got[1].Type != session.EventRequestResolved || got[3].Type != session.EventInputResolved {
				t.Fatal(got)
			}
		}},
		{"usage-replaces-repeated-message", []string{
			`{"type":"message.updated","properties":{"info":{"sessionID":"ses_fake","id":"msg_a","role":"assistant","time":{"completed":1},"tokens":{"input":2,"output":3}}}}`,
			`{"type":"message.updated","properties":{"info":{"sessionID":"ses_fake","id":"msg_a","role":"assistant","time":{"completed":1},"tokens":{"input":2,"output":3}}}}`,
			`{"type":"message.updated","properties":{"info":{"sessionID":"ses_fake","id":"msg_b","role":"assistant","time":{"completed":2},"tokens":{"input":4,"output":5}}}}`,
		}, func(t *testing.T, got []session.Event) {
			if len(got) != 3 || got[1].InputTokens != 2 || got[2].InputTokens != 6 || got[2].OutputTokens != 8 {
				t.Fatal(got)
			}
		}},
		{"assistant-aborted", []string{
			`{"type":"message.updated","properties":{"info":{"sessionID":"ses_fake","id":"msg_a","role":"assistant","error":{"name":"MessageAbortedError","data":{"message":"aborted"}}}}}`,
		}, func(t *testing.T, got []session.Event) {
			if len(got) != 1 || got[0].Type != session.EventTurnCompleted || got[0].State != session.TurnInterrupted {
				t.Fatal(got)
			}
		}},
		{"pending-input-resolved-on-idle", []string{
			`{"type":"question.asked","properties":{"sessionID":"ses_fake","id":"que","questions":[]}}`,
			`{"type":"session.status","properties":{"sessionID":"ses_fake","status":{"type":"idle"}}}`,
			`{"type":"session.idle","properties":{"sessionID":"ses_fake"}}`,
		}, func(t *testing.T, got []session.Event) {
			if len(got) != 3 || got[1].Type != session.EventInputResolved || got[2].State != session.TurnCompleted {
				t.Fatal(got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStream("ses_fake")
			s.begin("turn")
			var got []session.Event
			for _, record := range tc.records {
				if err := s.line([]byte(record), func(e session.Event) { got = append(got, e) }); err != nil {
					t.Fatal(err)
				}
			}
			tc.check(t, got)
		})
	}
}

func TestCloseReleasesBlockedSend(t *testing.T) {
	f := fake(t)
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/prompt_async") {
			io.Copy(io.Discard, r.Body)
			close(blocked)
			<-r.Context().Done()
			return
		}
		f.serveHTTP(w, r)
	}))
	defer srv.Close()
	events := make(chan session.Event, 100)
	d, err := (Provider{URL: srv.URL}).Start(context.Background(), session.Config{Cwd: "/workspace/with space"}, session.Origin{}, func(e session.Event) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(context.Background())
	ready(t, events)
	sent := make(chan error, 1)
	go func() { _, err := d.Send(context.Background(), session.Message{Text: "blocked"}); sent <- err }()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not reach server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	next(t, events, session.EventExited)
}

func TestStartValidationAndHandshake(t *testing.T) {
	for _, mode := range []string{"unhealthy", "missing-id", "event-refused", "event-timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := fake(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "unhealthy" && r.URL.Path == "/global/health" {
					json.NewEncoder(w).Encode(map[string]bool{"healthy": false})
					return
				}
				if mode == "missing-id" && r.URL.Path == "/session" {
					w.Write([]byte(`{}`))
					return
				}
				if r.URL.Path == "/event" {
					if mode == "event-refused" {
						w.WriteHeader(403)
						return
					}
					if mode == "event-timeout" {
						<-r.Context().Done()
						return
					}
				}
				f.serveHTTP(w, r)
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := (Provider{URL: srv.URL}).Start(ctx, session.Config{Cwd: "/workspace/with space"}, session.Origin{}, func(session.Event) {}); err == nil {
				t.Fatal("Start succeeded")
			}
		})
	}
}
