package web

// Commands (docs/web-protocol.md, Commands). Each one is the socket's Chat*
// message, which the daemon runs as it runs the socket's, so both check
// and answer alike. A command whose socket message does not exist yet
// answers unsupported from serve.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	chat "github.com/quanticstudios/pitwall/internal/session"
)

// commands are the ones chatCommand runs.
var commands = []string{"listSessions", "read", "start", "send", "interrupt", "updateQueue", "resolveApproval", "resolveInput", "set", "fork", "rewind", "archive", "unarchive", "rename", "pin", "listProjects", "listWorktrees", "import", "setJev", "checkpoints", "checkpointDiff", "rewindPreview"}

// frame is every command's keys; each reads its own.
type frame struct {
	FakeStartFailure string              `json:"fakeStartFailure"`
	Action           string              `json:"action"`
	MessageID        string              `json:"messageId"`
	Index            int                 `json:"index"`
	SessionID        string              `json:"sessionId"`
	TurnID           string              `json:"turnId"`
	After            int64               `json:"after"`
	Archived         bool                `json:"archived"`
	Dir              string              `json:"dir"`
	Provider         model.Provider      `json:"provider"`
	Model            string              `json:"model"`
	Effort           string              `json:"effort"`
	PermissionMode   string              `json:"permissionMode"`
	Worktree         string              `json:"worktree"`
	Base             string              `json:"base"`
	Branch           string              `json:"branch"`
	Name             string              `json:"name"`
	Prompt           string              `json:"prompt"`
	Text             string              `json:"text"`
	How              string              `json:"how"`
	RequestID        string              `json:"requestId"`
	Decision         string              `json:"decision"`
	Reason           string              `json:"reason"`
	Answers          map[string][]string `json:"answers"`
	Delete           bool                `json:"delete"`
	KeepFiles        bool                `json:"keepFiles"`
	Root             string              `json:"root"`
	Title            string              `json:"title"`
	Pinned           bool                `json:"pinned"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
	Snapshot         json.RawMessage     `json:"snapshot"`
	Attachments      []chat.Attachment   `json:"attachments"`
	Skill            *chat.Skill         `json:"skill"`
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
	case "listSessions", "start", "listProjects", "listWorktrees", "setJev":
	default:
		if f.SessionID == "" {
			c.reply(result(id, "invalid", typ+" needs a sessionId"))
			return true
		}
	}
	var m any
	switch typ {
	case "listSessions":
		chats := s.Chats.List()
		if f.Archived {
			chats = s.Chats.Archived()
		}
		c.reply(ok(id, "", map[string]any{"chats": s.entries(chats)}))
		return true
	case "import":
		c.reply(s.importChat(id, f))
		return true
	case "setJev":
		c.reply(s.setJev(id, c.name, msg))
		return true
	case "checkpoints", "checkpointDiff", "rewindPreview":
		c.reply(s.checkpoint(id, typ, f))
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
		if f.Skill != nil && (f.Skill.Name == "" || f.Skill.ID == "") {
			c.reply(result(id, "invalid", "a skill needs a provider, an id and a name"))
			return true
		}
		if typ == "start" && f.Skill != nil && f.Skill.Provider != f.Provider {
			c.reply(result(id, "invalid", fmt.Sprintf("the skill runs with %s, not %s", f.Skill.Provider, f.Provider)))
			return true
		}
		m = proto.ChatSend{Chat: f.SessionID, Text: f.Text, Attachments: f.Attachments, Skill: f.Skill, How: f.How}
		if typ == "start" {
			m = proto.ChatNew{Dir: f.Dir, Provider: f.Provider, Model: f.Model, Effort: f.Effort, Mode: f.PermissionMode,
				FakeStartFailure: f.FakeStartFailure, Worktree: f.Worktree, Base: f.Base, Branch: f.Branch, Name: f.Name, Prompt: f.Prompt, Attachments: f.Attachments, Skill: f.Skill}
			m = ClientStart{m.(proto.ChatNew), c.name}
		}
	case "updateQueue":
		if f.Action == "" {
			c.reply(result(id, "invalid", "updateQueue needs an action"))
			return true
		}
		m = proto.ChatQueue{Chat: f.SessionID, Action: f.Action, ID: f.MessageID, Text: f.Text, Index: f.Index}
	case "interrupt":
		m = proto.ChatInterrupt{Chat: f.SessionID}
	case "resolveApproval":
		m = proto.ChatApprove{Chat: f.SessionID, Request: f.RequestID, Decision: f.Decision, Reason: f.Reason, By: "web:" + c.name}
	case "resolveInput":
		m = proto.ChatAnswer{Chat: f.SessionID, Request: f.RequestID, Answers: f.Answers}
	case "set":
		m = proto.ChatSet{Chat: f.SessionID, Model: f.Model, Effort: f.Effort, Mode: f.PermissionMode, Provider: f.Provider}
	case "fork":
		m = proto.ChatFork{Chat: f.SessionID, Turn: f.TurnID, Worktree: f.Worktree}
	case "rewind":
		m = proto.ChatRewind{Chat: f.SessionID, Turn: f.TurnID, KeepFiles: f.KeepFiles}
	case "archive":
		m = proto.ChatArchive{Chat: f.SessionID, Delete: f.Delete}
	case "unarchive":
		m = proto.ChatUnarchive{Chat: f.SessionID}
	case "rename":
		m = proto.ChatRename{Chat: f.SessionID, Title: f.Title}
	case "pin":
		m = proto.ChatPin{Chat: f.SessionID, Pinned: f.Pinned}
	}
	r, _ := s.Chats.Do(m).(proto.ChatResult)
	if r.Code != "" {
		c.reply(result(id, r.Code, r.Message))
		return true
	}
	var data map[string]any
	switch {
	case typ == "start" || typ == "fork" || typ == "unarchive":
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

// ClientStart is a start command with the hello name of the client that
// sent it, which the socket's ChatNew does not carry: "aide" gives the
// chat aide's Jev gates.
type ClientStart struct {
	proto.ChatNew
	Client string
}

var jevKeyRe = regexp.MustCompile(`^[\x21-\x7e]+$`)

// setJev hands aide's TypeSafe key, consent list and gate settings to the
// chats' Jev gates (docs/web-protocol.md, setJev). The key stays in the
// daemon's memory: no reply, log or file carries it.
func (s *Server) setJev(id, client string, msg []byte) map[string]any {
	if client != "aide" {
		return result(id, "invalid", "setJev is aide's: only a client that says hello as aide sends it")
	}
	var f struct {
		APIKey        string   `json:"apiKey"`
		DisabledRoots []string `json:"disabledRoots"`
		Gates         struct {
			Global     decide.GateOverrides            `json:"global"`
			Workspaces map[string]decide.GateOverrides `json:"workspaces"`
		} `json:"gates"`
	}
	if err := json.Unmarshal(msg, &f); err != nil {
		return result(id, "invalid", "setJev: want an apiKey string, disabledRoots paths and gates layers")
	}
	if f.APIKey != "" && (len(f.APIKey) > 4096 || !jevKeyRe.MatchString(f.APIKey)) {
		return result(id, "invalid", "the apiKey is not 1 to 4096 printable ASCII characters")
	}
	if len(f.DisabledRoots) > 1000 || len(f.Gates.Workspaces) > 1000 {
		return result(id, "invalid", "at most 1000 disabledRoots and 1000 workspace gate layers")
	}
	for _, r := range f.DisabledRoots {
		if len(r) > 4096 || !filepath.IsAbs(r) {
			return result(id, "invalid", "each disabledRoots entry is an absolute path")
		}
	}
	if err := f.Gates.Global.Check(); err != nil {
		return result(id, "invalid", err.Error())
	}
	for _, o := range f.Gates.Workspaces {
		if err := o.Check(); err != nil {
			return result(id, "invalid", err.Error())
		}
	}
	r, _ := s.Chats.Do(decide.AideJev{Key: decide.Secret(f.APIKey), DisabledRoots: f.DisabledRoots, Global: f.Gates.Global, Workspaces: f.Gates.Workspaces}).(proto.ChatResult)
	if r.Code != "" {
		return result(id, r.Code, r.Message)
	}
	return ok(id, "", nil)
}

// importChat stores f's snapshot as an archived chat (docs/web-protocol.md,
// import), so aide's history outlives its own store.
func (s *Server) importChat(id string, f frame) map[string]any {
	var snap chat.Snapshot
	if f.Dir == "" || f.Provider == "" || len(f.Snapshot) == 0 {
		return result(id, "invalid", "import needs a dir, a provider and a snapshot")
	}
	if err := json.Unmarshal(f.Snapshot, &snap); err != nil {
		return result(id, "invalid", fmt.Sprintf("import: snapshot: %v", err))
	}
	if snap.SessionID != f.SessionID {
		return result(id, "invalid", fmt.Sprintf("import: the snapshot is session %q, not %q", snap.SessionID, f.SessionID))
	}
	cfg := chat.Config{Format: 1, ID: f.SessionID, Provider: f.Provider, Workspace: snap.WorkspaceID, Cwd: f.Dir,
		Model: f.Model, Effort: f.Effort, Mode: f.PermissionMode, Created: f.CreatedAt, Title: f.Title, Pinned: f.Pinned, Updated: f.UpdatedAt}
	imported, err := s.Chats.Import(cfg, snap)
	if err != nil {
		return result(id, code(err), err.Error())
	}
	return ok(id, "", map[string]any{"imported": imported})
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
