package agent

import (
	"encoding/json"
	"strings"
)

// StatuslineState is what Claude Code's statusLine JSON says about one
// session: its model, effort, context window and cost. Fields Claude Code
// leaves out stay zero.
type StatuslineState struct {
	SessionID   string  `json:"session_id,omitempty"`
	Transcript  string  `json:"transcript,omitempty"`
	Model       string  `json:"model,omitempty"`        // model.id, as claude-opus-5-5
	ModelName   string  `json:"model_name,omitempty"`   // model.display_name, as Opus 5.5
	Effort      string  `json:"effort,omitempty"`       // effort.level
	ContextUsed int64   `json:"context_used,omitempty"` // tokens in context now; 0 before the first reply
	ContextSize int64   `json:"context_size,omitempty"` // context_window.context_window_size
	CostUSD     float64 `json:"cost_usd,omitempty"`
	Version     string  `json:"version,omitempty"` // Claude Code's own
}

// ParseStatusline reads the JSON Claude Code passes a statusLine command;
// false when it names no model, as for any other JSON.
func ParseStatusline(data []byte) (StatuslineState, bool) {
	var in struct {
		SessionID  string `json:"session_id"`
		Transcript string `json:"transcript_path"`
		Version    string `json:"version"`
		Model      struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"model"`
		Effort struct {
			Level string `json:"level"`
		} `json:"effort"`
		Cost struct {
			USD float64 `json:"total_cost_usd"`
		} `json:"cost"`
		Context struct {
			Size    int64 `json:"context_window_size"`
			Current *struct {
				Input         int64 `json:"input_tokens"`
				CacheCreation int64 `json:"cache_creation_input_tokens"`
				CacheRead     int64 `json:"cache_read_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
	}
	if json.Unmarshal(data, &in) != nil || in.Model.ID == "" && in.Model.DisplayName == "" {
		return StatuslineState{}, false
	}
	st := StatuslineState{SessionID: in.SessionID, Transcript: in.Transcript, Model: in.Model.ID, ModelName: in.Model.DisplayName,
		Effort: in.Effort.Level, ContextSize: max(in.Context.Size, 0), CostUSD: max(in.Cost.USD, 0), Version: in.Version}
	if c := in.Context.Current; c != nil { // the same sum as a transcript's usage line
		st.ContextUsed = max(c.Input+c.CacheCreation+c.CacheRead, 0)
	}
	return st, true
}

// EncodeStatusline is the report-agent payload that carries st to the
// daemon. It has no name, so a daemon that predates it refuses the report
// as a malformed one and changes nothing.
func EncodeStatusline(st StatuslineState) []byte {
	b, _ := json.Marshal(struct {
		Statusline StatuslineState `json:"statusline"`
	}{st})
	return b
}

// DecodeStatusline reads an EncodeStatusline payload; false for any other.
func DecodeStatusline(payload []byte) (StatuslineState, bool) {
	var p struct {
		Name       string           `json:"name"`
		Statusline *StatuslineState `json:"statusline"`
	}
	if len(payload) > 16<<10 || json.Unmarshal(payload, &p) != nil || p.Statusline == nil || p.Name != "" {
		return StatuslineState{}, false
	}
	st := *p.Statusline
	st.Model, st.ModelName, st.Effort = strings.TrimSpace(st.Model), strings.TrimSpace(st.ModelName), strings.TrimSpace(st.Effort)
	return st, st.Model != "" || st.ModelName != ""
}
