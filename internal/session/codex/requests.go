package codex

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// request is a server request waiting for the user's answer.
type request struct {
	p     *proc
	rpcID json.RawMessage
	input bool // item/tool/requestUserInput, else an approval
}

// approvals are the server requests that become request.opened, as aide
// maps them.
var approvals = map[string]session.RequestType{
	"item/commandExecution/requestApproval": session.RequestCommand,
	"item/fileChange/requestApproval":       session.RequestFileChange,
	"item/fileRead/requestApproval":         session.RequestFileRead,
}

// request turns a server request into an approval or input event, under
// an id of pitwall's that Approve and Answer take. Anything else is
// refused at once, so the turn never waits on it.
func (d *driver) request(p *proc, rpcID json.RawMessage, method string, params json.RawMessage) {
	var n struct {
		ItemID, Command, Cwd, Reason string
		Questions                    []struct {
			ID, Header, Question string
			Options              []struct{ Label, Description string }
		}
	}
	json.Unmarshal(params, &n)
	var e session.Event
	typ, approval := approvals[method]
	switch {
	case approval:
		e.Type, e.RequestType, e.Detail = session.EventRequestOpened, typ, cmp.Or(n.Command, n.Reason)
		e.Request = &session.ToolRequest{ToolName: strings.Split(method, "/")[1], Command: n.Command, Cwd: n.Cwd, Input: params}
		// A file change request names its item; the item has the change.
		if c := d.tools[n.ItemID]; c != nil && len(c.Files) > 0 {
			e.Request.FilePath = c.Files[0].Path
			var it struct{ Changes []struct{ Diff string } }
			json.Unmarshal(c.Input, &it)
			if len(it.Changes) > 0 {
				e.Request.Diff = it.Changes[0].Diff
			}
		}
	case method == "item/tool/requestUserInput":
		e.Type, e.Detail, e.Questions = session.EventInputRequested, "Codex is waiting for user input.", []session.Question{}
		for i, q := range n.Questions {
			sq := session.Question{ID: cmp.Or(q.ID, fmt.Sprintf("question-%d", i+1)), Header: q.Header, Prompt: q.Question, Options: []session.PromptOption{}}
			for j, o := range q.Options {
				sq.Options = append(sq.Options, session.PromptOption{ID: fmt.Sprintf("option-%d", j+1), Label: o.Label, Description: o.Description})
			}
			e.Questions = append(e.Questions, sq)
		}
	default:
		p.write(message{ID: rpcID, Error: rpcError{Code: -32601, Message: "Unsupported request: " + method}})
		d.post(session.Event{Type: session.EventError, Message: "Codex asked for " + method + ", which pitwall does not handle; it was refused."})
		return
	}
	e.RequestID = newID()
	d.mu.Lock()
	d.requests[e.RequestID] = request{p: p, rpcID: rpcID, input: e.Type == session.EventInputRequested}
	d.mu.Unlock()
	d.post(e)
}

// resolved is the event that closes r in the snapshot.
func resolved(id string, r request) session.Event {
	if r.input {
		return session.Event{Type: session.EventInputResolved, RequestID: id}
	}
	return session.Event{Type: session.EventRequestResolved, RequestID: id}
}

// serverResolved settles a request the server no longer waits on: someone
// answered it, or the server withdrew it.
func (d *driver) serverResolved(params json.RawMessage) {
	var n struct{ RequestID json.RawMessage }
	json.Unmarshal(params, &n)
	d.mu.Lock()
	var events []session.Event
	for id, r := range d.requests {
		if string(r.rpcID) == string(n.RequestID) {
			delete(d.requests, id)
			events = append(events, resolved(id, r))
		}
	}
	d.mu.Unlock()
	for _, e := range events {
		d.post(e)
	}
}

// settleAll resolves every open request; a turn's end leaves none waiting.
func (d *driver) settleAll() {
	d.mu.Lock()
	var events []session.Event
	for _, id := range slices.Sorted(maps.Keys(d.requests)) {
		events = append(events, resolved(id, d.requests[id]))
	}
	clear(d.requests)
	d.mu.Unlock()
	for _, e := range events {
		d.post(e)
	}
}

// answer writes result to request id and resolves it. The caller holds
// cmdMu.
func (d *driver) answer(id string, input bool, result any) error {
	d.mu.Lock()
	r, ok := d.requests[id]
	if ok && r.input == input {
		delete(d.requests, id)
	}
	ended := ok && r.p.calls == nil
	d.mu.Unlock()
	switch {
	case !ok || r.input != input:
		return &session.ProviderError{Op: "answer", Message: "Codex is not waiting on request " + id}
	case ended:
		return session.ErrClosed
	}
	// Before the write, so it comes ahead of anything the answer causes.
	d.post(resolved(id, r))
	return r.p.write(message{ID: r.rpcID, Result: result})
}

// Approve answers an approval. A deny's reason has no field in the
// decision, so it follows as a steer the model reads in the same turn.
func (d *driver) Approve(ctx context.Context, id string, dec session.Decision) error {
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	decision := "decline"
	switch {
	case dec.Allow && dec.ForSession:
		decision = "acceptForSession"
	case dec.Allow:
		decision = "accept"
	}
	if err := d.answer(id, false, map[string]string{"decision": decision}); err != nil {
		return err
	}
	if !dec.Allow && dec.Reason != "" {
		return d.steer(ctx, session.Message{Text: "I declined that request: " + dec.Reason})
	}
	return nil
}

// Answer answers a user input request, by question id.
func (d *driver) Answer(ctx context.Context, id string, answers map[string][]string) error {
	d.cmdMu.Lock()
	defer d.cmdMu.Unlock()
	out := map[string]map[string][]string{}
	for q, a := range answers {
		out[q] = map[string][]string{"answers": append([]string{}, a...)}
	}
	return d.answer(id, true, map[string]any{"answers": out})
}

// newID names a request; tests replace it.
var newID = func() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
