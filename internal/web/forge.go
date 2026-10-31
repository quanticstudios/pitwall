package web

// Pull requests (docs/web-protocol.md, Pull requests): the pull request of
// each chat's tab rides on its list entry, and three commands read its
// review, build pitwall's message to the agent, and merge it.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Forge is the pull requests of the chats' tabs. A Chats that is also a
// Forge puts each tab's pull request on its chats in the list and serves
// forgeCommands. Each method but PRs names the tab by workspace id.
type Forge interface {
	// PRs is the pull request of each tab's branch, by workspace id, as
	// the daemon's poll last saw it.
	PRs() map[string]model.PR
	// Reviews is the unresolved review threads of the tab's pull request,
	// and the text of each standing change request.
	Reviews(ctx context.Context, workspace string) ([]forge.Thread, []forge.Comment, error)
	// Prompt is what Send failures (what "failures") or Send review
	// comments ("review") sends the tab's agent, "" with nothing to send.
	Prompt(ctx context.Context, workspace, what string) (string, error)
	// Merge merges the tab's pull request by [git] merge_method.
	Merge(ctx context.Context, workspace string) error
}

var forgeCommands = []string{"reviewThreads", "prPrompt", "mergePR"}

// forgeTimeout bounds one forge command: gh reads every failed log.
const forgeTimeout = 3 * time.Minute

// listed is a list entry with its pull request.
type listed struct {
	model.Chat
	PR *prEntry `json:"pr,omitempty"`
}

// prEntry is model.PR as the list carries it.
type prEntry struct {
	Number    int           `json:"number"`
	State     model.PRState `json:"state"`
	Draft     bool          `json:"draft"`
	URL       string        `json:"url"`
	Review    model.Review  `json:"review"`
	Checks    []prCheck     `json:"checks"`
	Conflicts bool          `json:"conflicts"`
	Head      string        `json:"head"`
	Reason    model.Reason  `json:"reason"`
	Detail    string        `json:"detail"`
}

type prCheck struct {
	Name  string           `json:"name"`
	State model.CheckState `json:"state"`
}

// entries are chats as the list sends them, with their tab's pull request
// when Chats is a Forge.
func (s *Server) entries(chats []model.Chat) []listed {
	var prs map[string]model.PR
	if f, ok := s.Chats.(Forge); ok {
		prs = f.PRs()
	}
	out := make([]listed, len(chats))
	for i, ch := range chats {
		out[i].Chat = ch
		if pr, ok := prs[ch.WorkspaceID]; ok {
			e := prEntry{Number: pr.Number, State: pr.State, Draft: pr.Draft, URL: pr.URL, Review: pr.Review,
				Checks: []prCheck{}, Conflicts: pr.Conflicts, Head: pr.Head, Reason: pr.Reason, Detail: pr.Detail}
			for _, c := range pr.Checks {
				e.Checks = append(e.Checks, prCheck{c.Name, c.State})
			}
			out[i].PR = &e
		}
	}
	return out
}

// same reports whether the client holding a needs b sent: their pull
// requests or any field of model.Chat but Seq differ.
func same(a, b listed) bool {
	a.Seq = b.Seq
	if a.Chat != b.Chat || (a.PR == nil) != (b.PR == nil) {
		return false
	}
	return a.PR == nil || string(mustJSON(a.PR)) == string(mustJSON(b.PR))
}

// forgeCommand runs a command of forgeCommands; false for any other, or
// when Chats is no Forge. gh can take a while, so the answer comes from
// a goroutine and later commands on c do not wait for it.
func (s *Server) forgeCommand(c *conn, typ, id string, msg []byte) bool {
	f, isForge := s.Chats.(Forge)
	if !isForge || !slices.Contains(forgeCommands, typ) {
		return false
	}
	var m struct {
		SessionID string `json:"sessionId"`
		What      string `json:"what"`
	}
	if err := json.Unmarshal(msg, &m); err != nil || m.SessionID == "" {
		c.reply(result(id, "invalid", typ+" needs a sessionId"))
		return true
	}
	if typ == "prPrompt" && m.What != "failures" && m.What != "review" {
		c.reply(result(id, "invalid", `prPrompt's what is "failures" or "review"`))
		return true
	}
	chats := s.Chats.List()
	i := slices.IndexFunc(chats, func(ch model.Chat) bool { return ch.ID == m.SessionID })
	if i < 0 {
		c.reply(result(id, "not-found", fmt.Sprintf("no chat %s", m.SessionID)))
		return true
	}
	ws := chats[i].WorkspaceID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), forgeTimeout)
		defer cancel()
		var data map[string]any
		var err error
		switch typ {
		case "reviewThreads":
			var threads []forge.Thread
			var changes []forge.Comment
			threads, changes, err = f.Reviews(ctx, ws)
			data = map[string]any{"threads": threadsJSON(threads), "changeRequests": commentsJSON(changes)}
		case "prPrompt":
			var text string
			text, err = f.Prompt(ctx, ws, m.What)
			data = map[string]any{"text": text}
		case "mergePR":
			err = f.Merge(ctx, ws)
		}
		if err != nil {
			c.reply(result(id, code(err), err.Error()))
			return
		}
		c.reply(ok(id, "", data))
	}()
	return true
}

func threadsJSON(ts []forge.Thread) []map[string]any {
	out := []map[string]any{}
	for _, t := range ts {
		out = append(out, map[string]any{"path": t.Path, "line": t.Line, "comments": commentsJSON(t.Comments), "more": t.More})
	}
	return out
}

func commentsJSON(cs []forge.Comment) []map[string]string {
	out := []map[string]string{}
	for _, c := range cs {
		out = append(out, map[string]string{"author": c.Author, "body": c.Body})
	}
	return out
}
