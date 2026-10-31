package web

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	chat "github.com/quanticstudios/pitwall/internal/session"
)

// checkpoint answers checkpoints, checkpointDiff and rewindPreview
// (docs/web-protocol.md, checkpoints) from chat f.SessionID's turns and
// its tab's checkpoints.
func (s *Server) checkpoint(id, typ string, f frame) map[string]any {
	d, err := s.Chats.Read(f.SessionID, 0)
	if err != nil {
		return result(id, code(err), err.Error())
	}
	var snap chat.Snapshot
	if err := json.Unmarshal(d.Snapshot, &snap); err != nil {
		return result(id, "provider", err.Error())
	}
	dir, ws, err := s.Chats.Tab(f.SessionID)
	if err != nil {
		return result(id, code(err), err.Error())
	}
	ctx := context.Background()
	if typ == "checkpoints" {
		list := []map[string]any{}
		if !slices.ContainsFunc(snap.Turns, func(t chat.TurnSummary) bool { return t.CheckpointID != "" }) {
			return ok(id, "", map[string]any{"checkpoints": list})
		}
		revs, err := gitstat.Checkpoints(ctx, dir, ws)
		if err != nil {
			return result(id, "conflict", err.Error())
		}
		for _, t := range snap.Turns {
			if t.CheckpointID != "" && slices.Contains(revs, t.CheckpointID) {
				list = append(list, map[string]any{"turnId": t.ID, "checkpointId": t.CheckpointID, "capturedAt": t.StartedAt})
			}
		}
		return ok(id, "", map[string]any{"checkpoints": list})
	}
	if f.TurnID == "" {
		return result(id, "invalid", typ+" needs a turnId")
	}
	i := slices.IndexFunc(snap.Turns, func(t chat.TurnSummary) bool { return t.ID == f.TurnID })
	if i < 0 {
		return result(id, "not-found", "no turn "+f.TurnID)
	}
	rev := snap.Turns[i].CheckpointID
	if rev == "" {
		return result(id, "unsupported", "the turn has no file checkpoint")
	}
	if typ == "rewindPreview" && snap.Busy() {
		return result(id, "busy", chat.ErrBusy.Error())
	}
	// The turn's change ends at the next checkpoint, the last turn's at the files now.
	next := ""
	if j := slices.IndexFunc(snap.Turns[i+1:], func(t chat.TurnSummary) bool { return t.CheckpointID != "" }); j >= 0 {
		next = snap.Turns[i+1+j].CheckpointID
	}
	patch, files, err := gitstat.CheckpointDiff(ctx, dir, rev, next)
	if err != nil {
		return result(id, "conflict", err.Error())
	}
	if typ == "checkpointDiff" {
		return ok(id, "", map[string]any{"patch": patch, "files": changes(files)})
	}
	back, backFiles, err := gitstat.CheckpointDiff(ctx, dir, "", rev)
	if err != nil {
		return result(id, "conflict", err.Error())
	}
	return ok(id, "", map[string]any{"patch": back, "files": changes(backFiles), "turnPatch": patch, "turnFiles": changes(files)})
}

// changes is cs as the protocol's files.
func changes(cs []gitstat.Change) []map[string]string {
	out := make([]map[string]string, len(cs))
	for i, c := range cs {
		out[i] = map[string]string{"path": c.Path, "status": c.Status}
		if c.From != "" {
			out[i]["from"] = c.From
		}
	}
	return out
}
