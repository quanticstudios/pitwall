package web

// Commands (docs/web-protocol.md, Commands). Each one is the socket's Chat*
// message, which the daemon runs as it runs the socket's, so both check
// and answer alike. A command whose socket message does not exist yet
// answers unsupported from serve.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// commands are the ones chatCommand runs. TODO(ENG-2691): fork and rewind
// once their socket messages land; unarchive with ChatUnarchive.
var commands = []string{"listSessions", "read", "start", "send", "interrupt", "resolveApproval", "resolveInput", "set", "archive", "listProjects", "listWorktrees"}

// aide's attachment limits.
const (
	maxAttachments = 8
	maxImage       = 5 << 20
	maxImages      = 10 << 20
)

// frame is every command's keys; each reads its own.
type frame struct {
	SessionID      string              `json:"sessionId"`
	After          int64               `json:"after"`
	Archived       bool                `json:"archived"`
	Dir            string              `json:"dir"`
	Provider       model.Provider      `json:"provider"`
	Model          string              `json:"model"`
	Effort         string              `json:"effort"`
	PermissionMode string              `json:"permissionMode"`
	Worktree       string              `json:"worktree"`
	Base           string              `json:"base"`
	Branch         string              `json:"branch"`
	Name           string              `json:"name"`
	Prompt         string              `json:"prompt"`
	Text           string              `json:"text"`
	How            string              `json:"how"`
	RequestID      string              `json:"requestId"`
	Decision       string              `json:"decision"`
	Reason         string              `json:"reason"`
	Answers        map[string][]string `json:"answers"`
	Delete         bool                `json:"delete"`
	Root           string              `json:"root"`
	Attachments    []struct {
		Kind string `json:"kind"`
		Data string `json:"data"`
	} `json:"attachments"`
}

// chatCommand runs a command of commands; false for any other.
func (s *Server) chatCommand(c *conn, typ, id string, msg []byte) bool {
	if !slices.Contains(commands, typ) {
		return false
	}
	var f frame
	if err := json.Unmarshal(msg, &f); err != nil {
		c.reply(result(id, "invalid", fmt.Sprintf("%s: %v", typ, err)))
		return true
	}
	switch typ {
	case "listSessions", "start", "listProjects", "listWorktrees":
	default:
		if f.SessionID == "" {
			c.reply(result(id, "invalid", typ+" needs a sessionId"))
			return true
		}
	}
	var m any
	switch typ {
	case "listSessions":
		if f.Archived {
			c.reply(result(id, "unsupported", "pitwall does not list archived chats yet"))
			return true
		}
		c.reply(ok(id, "", map[string]any{"chats": nonNil(s.Chats.List())}))
		return true
	case "read":
		if f.After < 0 {
			c.reply(result(id, "invalid", "after is 0 or more"))
			return true
		}
		d, err := s.Chats.Read(f.SessionID, f.After)
		if err != nil {
			c.reply(result(id, code(err), err.Error()))
			return true
		}
		data := map[string]any{"seq": d.Seq, "deltas": nonNil(rawJSON(d.Deltas))}
		if d.Snapshot != nil {
			data["snapshot"] = json.RawMessage(d.Snapshot)
		}
		c.reply(ok(id, "", data))
		return true
	case "listProjects":
		c.reply(ok(id, "", map[string]any{"projects": projects(s.Chats.Workspaces())}))
		return true
	case "listWorktrees":
		if f.Root == "" || !filepath.IsAbs(f.Root) {
			c.reply(result(id, "invalid", "listWorktrees needs a root, an absolute path"))
			return true
		}
		trees, err := gitstat.ListWorktrees(context.Background(), f.Root)
		if err != nil {
			c.reply(result(id, "invalid", fmt.Sprintf("%s: %v", f.Root, err)))
			return true
		}
		out := make([]map[string]string, len(trees))
		for i, t := range trees {
			out[i] = map[string]string{"path": t.Path, "branch": t.Branch, "head": t.Head}
		}
		c.reply(ok(id, "", map[string]any{"worktrees": out}))
		return true
	case "start", "send":
		if typ == "start" && (f.Dir == "" || f.Provider == "") {
			c.reply(result(id, "invalid", "start needs a dir and a provider"))
			return true
		}
		if err := attachments(f); err != "" {
			c.reply(result(id, "invalid", err))
			return true
		}
		if len(f.Attachments) > 0 {
			// shortcut: the chat loop takes no attachments yet (ChatNew.Images is refused); pass them once it does.
			c.reply(result(id, "unsupported", "attachments are not supported yet"))
			return true
		}
		m = proto.ChatSend{Chat: f.SessionID, Text: f.Text, How: f.How}
		if typ == "start" {
			m = proto.ChatNew{Dir: f.Dir, Provider: f.Provider, Model: f.Model, Effort: f.Effort, Mode: f.PermissionMode,
				Worktree: f.Worktree, Base: f.Base, Branch: f.Branch, Name: f.Name, Prompt: f.Prompt}
		}
	case "interrupt":
		m = proto.ChatInterrupt{Chat: f.SessionID}
	case "resolveApproval":
		m = proto.ChatApprove{Chat: f.SessionID, Request: f.RequestID, Decision: f.Decision, Reason: f.Reason, By: "web:" + c.name}
	case "resolveInput":
		m = proto.ChatAnswer{Chat: f.SessionID, Request: f.RequestID, Answers: f.Answers}
	case "set":
		m = proto.ChatSet{Chat: f.SessionID, Model: f.Model, Effort: f.Effort, Mode: f.PermissionMode}
	case "archive":
		m = proto.ChatArchive{Chat: f.SessionID, Delete: f.Delete}
	}
	r, _ := s.Chats.Do(m).(proto.ChatResult)
	if r.Code != "" {
		c.reply(result(id, r.Code, r.Message))
		return true
	}
	var data map[string]any
	switch {
	case typ == "start":
		data = map[string]any{"sessionId": r.Chat}
		chats := s.Chats.List()
		if i := slices.IndexFunc(chats, func(ch model.Chat) bool { return ch.ID == r.Chat }); i >= 0 {
			data["workspaceId"] = chats[i].WorkspaceID
		}
	case r.Turn != "":
		data = map[string]any{"turnId": r.Turn}
	}
	c.reply(ok(id, r.Message, data))
	return true
}

// attachments is why f's attachments break aide's limits, "" when they do
// not.
func attachments(f frame) string {
	if len(f.Attachments) > maxAttachments {
		return fmt.Sprintf("%d attachments: at most %d", len(f.Attachments), maxAttachments)
	}
	total := 0
	for _, a := range f.Attachments {
		if a.Kind != "image" {
			continue
		}
		n := base64.StdEncoding.DecodedLen(len(a.Data))
		if n > maxImage {
			return "an image over 5 MiB"
		}
		total += n
	}
	if total > maxImages {
		return "images over 10 MiB in all"
	}
	return ""
}

// projects groups the tabs by repo root, in the order the first of each
// comes.
func projects(ws []model.Workspace) []map[string]any {
	out := []map[string]any{}
	at := map[string]int{}
	for _, w := range ws {
		root := w.RepoRoot
		if root == "" {
			root = w.Path
		}
		i, seen := at[root]
		if !seen {
			i, at[root] = len(out), len(out)
			out = append(out, map[string]any{"root": root, "name": filepath.Base(root), "workspaces": []map[string]any{}})
		}
		out[i]["workspaces"] = append(out[i]["workspaces"].([]map[string]any), map[string]any{
			"id": w.ID, "label": w.Label, "branch": w.Branch, "path": w.Path,
			"worktree": w.WorktreeRoot != "", "pinned": w.Pinned, "chat": w.Chat,
		})
	}
	return out
}

func ok(id, message string, data map[string]any) map[string]any {
	r := result(id, "", "")
	if message != "" {
		r["message"] = message
	}
	if data != nil {
		r["data"] = data
	}
	return r
}

func rawJSON(bs [][]byte) []json.RawMessage {
	out := make([]json.RawMessage, len(bs))
	for i, b := range bs {
		out[i] = b
	}
	return out
}
