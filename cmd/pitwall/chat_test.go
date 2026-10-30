package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// chatTestState has session main with a tab of panes (#1) and a chat tab (#2).
func chatTestState() model.State {
	return model.State{
		Sessions: []model.Session{{ID: "m", Name: "main", Order: []string{"w", "c"}}},
		Workspaces: []model.Workspace{
			{ID: "w", SessionID: "m", Name: "shell", NameSet: true, Path: "/tmp"},
			{ID: "c", SessionID: "m", Label: "Fix the build", Path: "/tmp", Chat: "0f3e2a10-aaaa-4bbb-8ccc-000000000001"},
		},
		Chats: []model.Chat{{ID: "0f3e2a10-aaaa-4bbb-8ccc-000000000001", WorkspaceID: "c", Provider: model.ProviderCodex, Title: "Fix the build", State: "waiting", Pending: 1}},
	}
}

func TestListTabsChats(t *testing.T) {
	var out bytes.Buffer
	if err := listTabs(&out, chatTestState(), "m", nil, false); err != nil {
		t.Fatal(err)
	}
	tabs, chats, ok := strings.Cut(out.String(), "\n\n")
	if !ok || strings.Contains(tabs, "Fix the build") || !strings.Contains(tabs, "shell") {
		t.Fatalf("tabs section:\n%s", out.String())
	}
	if fields := strings.Fields(strings.Split(chats, "\n")[1]); !reflect.DeepEqual(fields, []string{"2", "0f3e2a10", "codex", "waiting", "1", "Fix", "the", "build"}) {
		t.Fatalf("chats section:\n%s", chats)
	}
}

func TestResolveChat(t *testing.T) {
	s := chatTestState()
	for _, name := range []string{"0f3e2a10-aaaa-4bbb-8ccc-000000000001", "0f3e", "#2", "Fix the build"} {
		if c, err := resolveChat(s, "m", name); err != nil || c.WorkspaceID != "c" {
			t.Errorf("%q: %+v, %v", name, c, err)
		}
	}
	for name, want := range map[string]string{"#1": "not a chat", "nothing": "no chat matches"} {
		if _, err := resolveChat(s, "m", name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", name, err, want)
		}
	}
}

func TestChatState(t *testing.T) {
	for c, want := range map[model.Chat]string{
		{State: "running"}:             "working",
		{State: "connecting"}:          "working",
		{State: "waiting", Pending: 1}: "blocked",
		{State: "ready"}:               "done",
		{State: "error"}:               "done",
		{State: "stopped"}:             "done",
	} {
		if got := chatState(c); got != want {
			t.Errorf("%+v: %s, want %s", c, got, want)
		}
	}
}

func TestPrintTranscript(t *testing.T) {
	detail := "make test"
	s := session.Snapshot{
		Content: []session.Item{
			{Kind: session.KindUserPrompt, Text: "Fix it\nplease"},
			{Kind: session.KindReasoningText, Text: "hidden"},
			{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Bash", Command: "make", Status: "completed"}},
			{Kind: session.KindAssistantText, Text: "Done."},
		},
		PendingApprovals: []session.Approval{{ID: "r1", RequestType: session.RequestCommand, Detail: &detail}},
	}
	var out bytes.Buffer
	if err := printTranscript(&out, s); err != nil {
		t.Fatal(err)
	}
	want := "> Fix it\n> please\n\n[completed] Bash make\n\nDone.\n\napproval r1: command-execution-approval make test\n"
	if out.String() != want {
		t.Fatalf("got\n%q\nwant\n%q", out.String(), want)
	}
}

func TestChatApproveInPane(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "/nonexistent")
	t.Setenv("PITWALL_PANE", "abc")
	code, _, stderr := cliOutput("chat", "approve", "0f3e", "allow")
	if code != waitError || !strings.Contains(stderr, "does not answer another agent") {
		t.Fatalf("code %d, %q", code, stderr)
	}
}

// chatDaemon is a daemon at level that answers Sync with state and each
// Chat* request with reply(request).
func chatDaemon(t *testing.T, level int, state model.State, reply func(any) any) {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("PITWALL_PANE", "")
	path := sockPath(t, "chat.sock")
	t.Setenv("PITWALL_SOCKET", path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				nc.SetDeadline(time.Now().Add(5 * time.Second))
				c := proto.NewConn(nc)
				for {
					m, err := c.Recv()
					if err != nil {
						return
					}
					switch m.(type) {
					case proto.Hello:
					case proto.Sync:
						c.Send(proto.StateMsg{State: state, Level: level})
					default:
						c.Send(reply(m))
					}
				}
			}()
		}
	}()
}

func TestChatCLI(t *testing.T) {
	var mu sync.Mutex
	var got []any
	chatDaemon(t, proto.Level, chatTestState(), func(m any) any {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		switch m := m.(type) {
		case proto.ChatNew:
			return proto.ChatResult{Chat: "new-id", Turn: "t1"}
		case proto.ChatSend:
			return proto.ChatResult{Chat: m.Chat, Message: "queued"}
		case proto.ChatApprove:
			return proto.ChatResult{Chat: m.Chat, Code: "conflict", Message: "someone answered first"}
		case proto.ChatRead:
			b, _ := json.Marshal(session.Snapshot{Content: []session.Item{{Kind: session.KindAssistantText, Text: "Hi."}}})
			return proto.ChatDeltas{Chat: m.Chat, Snapshot: b, Seq: 3}
		}
		return proto.Error{Message: fmt.Sprintf("unexpected %T", m)}
	})
	id := "0f3e2a10-aaaa-4bbb-8ccc-000000000001"
	dir := t.TempDir()
	for _, c := range []struct {
		args       []string
		code       int
		out        string
		request    any
		stderrHint string
	}{
		{[]string{"chat", "new", "--provider", "codex", "--model", "m", "--dir", dir, "--", "fix", "it"}, 0, "new-id\n",
			proto.ChatNew{SessionID: "m", Dir: dir, Provider: model.ProviderCodex, Model: "m", Prompt: "fix it"}, ""},
		{[]string{"chat", "send", "#2", "--queue", "--", "more"}, 0, "queued\n", proto.ChatSend{Chat: id, Text: "more", How: "queue"}, ""},
		{[]string{"chat", "approve", "0f3e", "r1", "deny", "--reason", "no"}, 1, "",
			proto.ChatApprove{Chat: id, Request: "r1", Decision: "deny", Reason: "no", By: "cli"}, "conflict: someone answered first"},
		{[]string{"chat", "read", "0f3e"}, 0, "Hi.\n\n", proto.ChatRead{Chat: id}, ""},
		{[]string{"chat", "ls", "--json"}, 0, "", nil, ""},
	} {
		mu.Lock()
		got = nil
		mu.Unlock()
		code, out, stderr := cliOutput(c.args...)
		mu.Lock()
		sent := got
		mu.Unlock()
		if code != c.code || c.out != "" && out != c.out || !strings.Contains(stderr, c.stderrHint) {
			t.Errorf("%q: code %d, out %q, stderr %q", c.args, code, out, stderr)
		}
		if c.request != nil && (len(sent) != 1 || !reflect.DeepEqual(sent[0], c.request)) {
			t.Errorf("%q: sent %#v, want %#v", c.args, sent, c.request)
		}
		if c.args[1] == "ls" {
			var chats []model.Chat
			if err := json.Unmarshal([]byte(out), &chats); err != nil || len(chats) != 1 || chats[0].Title != "Fix the build" {
				t.Errorf("ls --json: %q, %v", out, err)
			}
		}
	}
}

func TestChatOldDaemon(t *testing.T) {
	chatDaemon(t, proto.ChatLevel-1, chatTestState(), func(any) any { return proto.Error{Message: "unknown"} })
	if code, _, stderr := cliOutput("chat", "ls"); code != waitError || !strings.Contains(stderr, "predates chats") {
		t.Fatalf("code %d, %q", code, stderr)
	}
}
