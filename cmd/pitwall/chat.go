package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

const chatUsage = `usage:
  pitwall chat new [--provider ID] [--model M] [--effort E] [--mode M]
                   [--dir D | --worktree NAME [--base B] | --branch B] [--name N] [-- prompt]
  pitwall chat providers [--json]
  pitwall chat ls [--json]
  pitwall chat send <chat> [--steer|--queue] -- text
  pitwall chat approve <chat> [<request>] allow|allow-session|deny [--reason R]
  pitwall chat interrupt <chat>
  pitwall chat set <chat> [--model M] [--effort E] [--mode M]
  pitwall chat read <chat> [--json]
  pitwall chat wait <chat> [--until done|blocked] [--timeout D]
  pitwall chat archive <chat>
A chat is its id, a unique id prefix, or its tab (#N or name).`

// chatCommand is pitwall chat; wait exits as pitwall wait does.
func chatCommand(args []string, out, errOut io.Writer) int {
	code, err := runChat(args, out, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
	}
	return code
}

func runChat(args []string, out, errOut io.Writer) (int, error) {
	if len(args) == 0 {
		return waitError, errors.New(chatUsage)
	}
	sub, args := args[0], args[1:]
	flags := flag.NewFlagSet("chat "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var provider, modelName, effort, mode, dir, worktree, base, branch, name, reason, sessionName string
	var steer, queue, asJSON bool
	until := "done"
	var timeout time.Duration
	flags.StringVar(&sessionName, "s", "", "")
	switch sub {
	case "new":
		flags.StringVar(&provider, "provider", "", "")
		flags.StringVar(&dir, "dir", ".", "")
		flags.StringVar(&worktree, "worktree", "", "")
		flags.StringVar(&base, "base", "", "")
		flags.StringVar(&branch, "branch", "", "")
		flags.StringVar(&name, "name", "", "")
		fallthrough
	case "set":
		flags.StringVar(&modelName, "model", "", "")
		flags.StringVar(&effort, "effort", "", "")
		flags.StringVar(&mode, "mode", "", "")
	case "ls", "read", "providers":
		flags.BoolVar(&asJSON, "json", false, "")
	case "send":
		flags.BoolVar(&steer, "steer", false, "")
		flags.BoolVar(&queue, "queue", false, "")
	case "approve":
		flags.StringVar(&reason, "reason", "", "")
	case "wait":
		flags.StringVar(&until, "until", "done", "")
		flags.DurationVar(&timeout, "timeout", 0, "")
	case "interrupt", "archive":
	default:
		return waitError, fmt.Errorf("unknown chat command %q\n%s", sub, chatUsage)
	}
	args, err := parseFlags(flags, args)
	if err != nil {
		return waitError, err
	}
	switch {
	case sub == "new" || sub == "send" && len(args) >= 2:
	case (sub == "ls" || sub == "providers") && len(args) == 0:
	case sub == "approve" && (len(args) == 2 || len(args) == 3):
	case sub == "wait" && len(args) == 1 && (until == "done" || until == "blocked"):
	case (sub == "interrupt" || sub == "set" || sub == "read" || sub == "archive") && len(args) == 1:
	default:
		return waitError, errors.New(chatUsage)
	}
	if sub == "approve" && os.Getenv("PITWALL_PANE") != "" {
		return waitError, errors.New("an agent in a pitwall pane does not answer another agent's approvals; the user does")
	}
	if sub == "wait" {
		return chatWait(args[0], sessionName, until, timeout, out)
	}

	conn, err := dialCLI()
	if err != nil {
		return waitError, err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return waitError, err
	}
	if m.Level < proto.ChatLevel {
		return waitError, errors.New("the running pitwall daemon predates chats; restart it to update it (pitwall doctor)")
	}
	if sub == "providers" {
		if m.Level < proto.ChatProvidersLevel {
			return waitError, errors.New("restart the daemon to list chat providers")
		}
		if err := conn.Send(proto.ChatProviders{}); err != nil {
			return waitError, err
		}
		reply, err := chatReply(conn)
		if err != nil {
			return waitError, err
		}
		list, ok := reply.(proto.ChatProviderList)
		if !ok {
			return waitError, fmt.Errorf("unexpected provider reply %T", reply)
		}
		if asJSON {
			err = json.NewEncoder(out).Encode(list.Providers)
		} else {
			for _, p := range list.Providers {
				if _, err = fmt.Fprintf(out, "%s\t%s\n", p.ID, p.Name); err != nil {
					break
				}
			}
		}
		return code(err), err
	}
	state := m.State
	sess, err := currentSession(state, sessionName)
	if err != nil && (sessionName != "" || sub != "ls" && sub != "new") {
		return waitError, err
	}
	if sub == "new" {
		if dir, err = filepath.Abs(dir); err != nil {
			return waitError, err
		}
		r, err := chatCall(conn, proto.ChatNew{
			SessionID: sess.ID, Dir: dir, Worktree: worktree, Base: base, Branch: branch,
			Provider: model.Provider(provider), Model: modelName, Effort: effort, Mode: mode,
			Prompt: strings.Join(args, " "), Name: name, ParentPane: os.Getenv("PITWALL_PANE"),
		})
		if r.Chat != "" {
			fmt.Fprintln(out, r.Chat)
		}
		if err == nil && r.Message != "" {
			fmt.Fprintln(errOut, r.Message)
		}
		return code(err), err
	}
	if sub == "ls" {
		chats := slices.DeleteFunc(slices.Clone(state.Chats), func(c model.Chat) bool { return state.SessionOf(c.WorkspaceID) != sess.ID })
		if asJSON {
			return code(json.NewEncoder(out).Encode(nonNilChats(chats))), nil
		}
		return code(listChats(out, state, sess.ID, chats)), nil
	}

	c, err := resolveChat(state, sess.ID, args[0])
	if err != nil {
		return waitError, err
	}
	var request any
	switch sub {
	case "send":
		how := ""
		if steer {
			how = "steer"
		} else if queue {
			how = "queue"
		}
		request = proto.ChatSend{Chat: c.ID, Text: strings.Join(args[1:], " "), How: how}
	case "approve":
		ask := proto.ChatApprove{Chat: c.ID, Decision: args[len(args)-1], Reason: reason, By: "cli"}
		if len(args) == 3 {
			ask.Request = args[1]
		}
		request = ask
	case "interrupt":
		request = proto.ChatInterrupt{Chat: c.ID}
	case "set":
		request = proto.ChatSet{Chat: c.ID, Model: modelName, Effort: effort, Mode: mode}
	case "archive":
		request = proto.ChatArchive{Chat: c.ID}
	case "read":
		if err := conn.Send(proto.ChatRead{Chat: c.ID}); err != nil {
			return waitError, err
		}
		reply, err := chatReply(conn)
		if err != nil {
			return waitError, err
		}
		d, ok := reply.(proto.ChatDeltas)
		if !ok {
			return waitError, errors.New(reply.(proto.ChatResult).Message)
		}
		if asJSON {
			_, err = fmt.Fprintf(out, "%s\n", d.Snapshot)
			return code(err), err
		}
		var snap session.Snapshot
		if err := json.Unmarshal(d.Snapshot, &snap); err != nil {
			return waitError, err
		}
		return code(printTranscript(out, snap)), nil
	}
	r, err := chatCall(conn, request)
	if err == nil {
		switch {
		case r.Turn != "":
			fmt.Fprintln(out, r.Turn)
		case r.Message != "":
			fmt.Fprintln(out, r.Message)
		}
	}
	return code(err), err
}

func code(err error) int {
	if err != nil {
		return waitError
	}
	return waitReached
}

func nonNilChats(c []model.Chat) []model.Chat {
	if c == nil {
		return []model.Chat{}
	}
	return c
}

// chatCall sends a Chat* request and returns its ChatResult, an error
// when it failed.
func chatCall(conn *cliConn, request any) (proto.ChatResult, error) {
	if err := conn.Send(request); err != nil {
		return proto.ChatResult{}, err
	}
	reply, err := chatReply(conn)
	if err != nil {
		return proto.ChatResult{}, err
	}
	r, ok := reply.(proto.ChatResult)
	if !ok {
		return r, fmt.Errorf("unexpected reply %T", reply)
	}
	if r.Code != "" {
		return r, fmt.Errorf("%s: %s", r.Code, r.Message)
	}
	return r, nil
}

// chatReply is the daemon's reply to the request just sent.
func chatReply(conn *cliConn) (any, error) {
	if err := conn.socket.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, err
	}
	for {
		msg, err := conn.Recv()
		if err != nil {
			return nil, err
		}
		switch m := msg.(type) {
		case proto.Error:
			return nil, errors.New(m.Message)
		case proto.ChatResult, proto.ChatDeltas, proto.ChatProviderList:
			return m, nil
		}
	}
}

// resolveChat finds a chat by id, unique id prefix, or tab.
func resolveChat(state model.State, session, name string) (model.Chat, error) {
	var prefixed []model.Chat
	for _, c := range state.Chats {
		if c.ID == name {
			return c, nil
		}
		if strings.HasPrefix(c.ID, name) {
			prefixed = append(prefixed, c)
		}
	}
	if len(prefixed) == 1 {
		return prefixed[0], nil
	}
	w, err := resolveTab(state, session, name)
	if err != nil {
		if len(prefixed) > 1 {
			return model.Chat{}, fmt.Errorf("ambiguous chat %q; matches %d chats", name, len(prefixed))
		}
		return model.Chat{}, fmt.Errorf("no chat matches %q (see pitwall chat ls)", name)
	}
	if i := slices.IndexFunc(state.Chats, func(c model.Chat) bool { return c.ID == w.Chat }); w.Chat != "" && i >= 0 {
		return state.Chats[i], nil
	}
	return model.Chat{}, fmt.Errorf("tab %s is not a chat", tabTitle(w))
}

// listChats prints chats under their tab numbers.
func listChats(out io.Writer, state model.State, session string, chats []model.Chat) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "#\tCHAT\tPROVIDER\tSTATE\tPENDING\tTITLE"); err != nil {
		return err
	}
	tabs := numbered(state, session)
	for _, c := range chats {
		n := "-"
		if i := slices.IndexFunc(tabs, func(w model.Workspace) bool { return w.ID == c.WorkspaceID }); i >= 0 {
			n = strconv.Itoa(i + 1)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", n, c.ID[:min(8, len(c.ID))], c.Provider, c.State, c.Pending, c.Title); err != nil {
			return err
		}
	}
	return w.Flush()
}

// printTranscript prints a chat as text: prompts, assistant text, a line
// per tool call, and what waits on an answer, with its id.
func printTranscript(out io.Writer, s session.Snapshot) error {
	var b strings.Builder
	for _, it := range s.Content {
		switch it.Kind {
		case session.KindUserPrompt:
			fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(it.Text, "\n", "\n> "))
		case session.KindAssistantText:
			fmt.Fprintf(&b, "%s\n\n", it.Text)
		case session.KindSystem:
			fmt.Fprintf(&b, "-- %s\n\n", it.Text)
		case session.KindToolCall:
			if t := it.Tool; t != nil {
				fmt.Fprintf(&b, "[%s] %s %s\n\n", t.Status, t.ToolName, t.Command)
			}
		}
	}
	for _, a := range s.PendingApprovals {
		fmt.Fprintf(&b, "approval %s: %s %s\n", a.ID, a.RequestType, deref(a.Detail))
	}
	for _, in := range s.PendingUserInputs {
		fmt.Fprintf(&b, "input %s: %s\n", in.ID, deref(in.Detail))
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// chatState is a chat as pitwall chat wait sees it: blocked on an
// answer, working, or done.
func chatState(c model.Chat) string {
	switch {
	case c.Pending > 0:
		return "blocked"
	case c.State == session.StateConnecting || c.State == session.StateRunning || c.State == session.StateWaiting:
		return "working"
	}
	return "done"
}

// chatWait waits until the chat gets to until; a chat that blocks ends a
// wait for done, as pitwall wait does.
func chatWait(name, sessionName, until string, timeout time.Duration, out io.Writer) (int, error) {
	watch, err := dialWatch()
	if err != nil {
		return waitError, err
	}
	defer watch.Close()
	var timedOut atomic.Bool
	if timeout > 0 {
		t := time.AfterFunc(timeout, func() { timedOut.Store(true); watch.Close() })
		defer t.Stop()
	}
	state, err := watch.next()
	if err != nil {
		return waitError, err
	}
	if watch.level < proto.ChatLevel {
		return waitError, errors.New("the running pitwall daemon predates chats; restart it to update it (pitwall doctor)")
	}
	sess, err := currentSession(state, sessionName)
	if err != nil {
		return waitError, err
	}
	c, err := resolveChat(state, sess.ID, name)
	if err != nil {
		return waitError, err
	}
	for {
		i := slices.IndexFunc(state.Chats, func(x model.Chat) bool { return x.ID == c.ID })
		if i < 0 {
			return waitError, fmt.Errorf("chat %s was archived", c.ID)
		}
		switch st := chatState(state.Chats[i]); {
		case st == until:
			fmt.Fprintln(out, state.Chats[i].State)
			return waitReached, nil
		case st == "blocked":
			fmt.Fprintf(out, "blocked: %d pending\n", state.Chats[i].Pending)
			return waitBlocked, nil
		}
		if state, err = watch.next(); err != nil {
			if timedOut.Load() {
				return waitTimeout, fmt.Errorf("timed out after %s", timeout)
			}
			return waitError, err
		}
	}
}
