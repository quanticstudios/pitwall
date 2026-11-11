package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func questionScreen(t *testing.T, f *fakes, file string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	f.pane(0).mu.Lock()
	f.pane(0).screen = strings.Split(strings.TrimSpace(string(b)), "\n")
	f.pane(0).mu.Unlock()
}

func TestQuestionHoldsMessages(t *testing.T) {
	for _, state := range []model.AgentState{model.StateWorking, model.StateAwaitingInput} {
		for _, phone := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/phone=%t", state, phone), func(t *testing.T) {
				d, f, _ := remoteDaemon(t)
				d.mu.Lock()
				d.st.Panes[0].Provider = model.ProviderCodex
				d.st.Activities = []model.Activity{{PaneID: "a", Provider: model.ProviderCodex, State: state}}
				d.mu.Unlock()
				questionScreen(t, f, "codex-ask-notes.txt")
				if _, err := d.readyWindow("a", phone); err == nil {
					t.Fatal("question accepted a message")
				}
				var err error
				if phone {
					_, err = d.phoneSend("a", "use water")
				} else {
					err = d.windowDeliver(proto.Deliver{Pane: "a", Text: "use water"})
				}
				if err != nil {
					t.Fatal(err)
				}
				d.deliver("a")
				d.mu.Lock()
				queued := len(d.mail.queue["a"])
				d.mu.Unlock()
				if got := f.pane(0).got(); got != "" || queued != 1 {
					t.Fatalf("question hold: typed %q, queued %d", got, queued)
				}
			})
		}
	}
}

func TestPhoneQuestion(t *testing.T) {
	for _, tc := range []struct {
		file       string
		option     int
		text, want string
	}{
		{"claude-ask-single.txt", 1, "", "\x1b[B\r"},
		{"claude-ask-multi.txt", 1, "", "\x1b[B "},
		{"claude-ask-multi.txt", -1, "", "\x1b[B\x1b[B\x1b[B\x1b[B\r"},
		{"claude-ask-several.txt", 0, "", "\r"},
		{"claude-ask-other.txt", -2, "Medium", "Medium\r"},
		{"claude-ask-review.txt", 0, "", "\r"},
		{"codex-ask-single.txt", 1, "", "\x1b[B\r"},
		{"codex-ask-several.txt", 0, "", "\r"},
		{"codex-ask-notes.txt", -2, "Water", "\t\x1b[B\x1b[B\tWater\r"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			questionScreen(t, f, tc.file)
			prov := model.ProviderClaude
			if strings.HasPrefix(tc.file, "codex-") {
				prov = model.ProviderCodex
				d.mu.Lock()
				d.st.Panes[0].Provider = prov
				d.mu.Unlock()
			}
			q := agent.ReadQuestion(prov, f.pane(0).Snapshot())
			if q == nil {
				t.Fatal("no question")
			}
			before := len(f.pane(0).got())
			if err := d.phoneQuestion("a", q.ID, tc.option, tc.text); err != nil {
				t.Fatal(err)
			}
			if got := gotAfter(t, f.pane(0), before, len(tc.want)); got != tc.want {
				t.Errorf("keys %q want %q", got, tc.want)
			}
			before = len(f.pane(0).got())
			if err := d.phoneQuestion("a", "stale", tc.option, tc.text); err == nil {
				t.Fatal("accepted stale question")
			}
			if f.pane(0).got()[before:] != "" {
				t.Fatal("stale answer typed")
			}
		})
	}
}

func TestPhoneUploadAndSendImage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d, f, call := remoteDaemon(t)
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderClaude
	d.mu.Unlock()
	png := []byte("\x89PNG\r\n\x1a\n")
	if code, _ := call("POST", "/api/upload?pane=a", string(png)); code != 200 {
		t.Fatalf("upload %d", code)
	}
	files, err := filepath.Glob(filepath.Join(pasteDir("w"), "*"))
	if err != nil || len(files) != 1 {
		t.Fatalf("paste files %v %v", files, err)
	}
	if b, err := os.ReadFile(files[0]); err != nil || string(b) != string(png) {
		t.Fatal("image bytes not saved")
	}
	if info, _ := os.Stat(files[0]); info.Mode().Perm() != 0600 {
		t.Fatal("image is not private")
	}
	if code, _ := call("POST", "/api/upload?pane=a", "plain text"); code != 400 {
		t.Fatalf("text upload %d", code)
	}
	if _, err := d.phoneSendImage("a", "look", "../outside.png"); err == nil {
		t.Fatal("accepted traversal")
	}
	if _, err := d.phoneSendImage("b", "look", filepath.Base(files[0])); err != nil {
		t.Fatal(err)
	} // same tab owns both panes
	if _, err := d.phoneSendImage("c", "look", "missing.png"); err == nil {
		t.Fatal("accepted missing image")
	}
	// A valid image waits behind the form, then uses the existing image delivery queue.
	d.mu.Lock()
	q := d.mail.queue["b"]
	d.mu.Unlock()
	if len(q) != 1 || len(q[0].images) != 1 || q[0].images[0] != files[0] {
		t.Fatalf("queued images %+v", q)
	}
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StateCompleted)}); err != nil {
		t.Fatal(err)
	}
	_ = f
}
