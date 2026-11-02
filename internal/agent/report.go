package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Report is the payload of pitwall report-agent, carried by AgentEvent.
// A nil ResumeCmd keeps the command; an empty slice clears it.
type Report struct {
	Name      string   `json:"name"`
	State     string   `json:"state,omitempty"`
	Message   string   `json:"message,omitempty"`
	ResumeCmd []string `json:"resume_cmd"`
	Release   bool     `json:"release,omitempty"`
}

// DecodeReport validates the whole report before the daemon applies any of it.
func DecodeReport(payload []byte) (Report, error) {
	var r Report
	if len(payload) > 16<<10 {
		return r, errors.New("agent report exceeds 16 KiB")
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return r, err
	}
	if r.Name == "" || strings.TrimSpace(r.Name) != r.Name || len(r.Name) > 64 || !utf8.ValidString(r.Name) || strings.ContainsFunc(r.Name, unicode.IsControl) {
		return r, errors.New("agent name must be 1 to 64 bytes without control characters or surrounding space")
	}
	switch r.State {
	case "", "working", "blocked", "done", "idle":
	default:
		return r, errors.New("agent state must be working, blocked, done or idle")
	}
	if len(r.Message) > 1024 || !utf8.ValidString(r.Message) || strings.ContainsFunc(r.Message, unicode.IsControl) {
		return r, errors.New("agent message must be at most 1024 bytes without control characters")
	}
	if r.Release && (r.State != "" || r.Message != "" || r.ResumeCmd != nil) {
		return r, errors.New("release cannot include state, message or resume command")
	}
	if r.State == "" && r.Message != "" {
		return r, errors.New("a message needs a state")
	}
	if len(r.ResumeCmd) > 64 {
		return r, errors.New("resume command exceeds 64 arguments")
	}
	size := 0
	for i, arg := range r.ResumeCmd {
		size += len(arg)
		if !utf8.ValidString(arg) || strings.ContainsFunc(arg, unicode.IsControl) || i == 0 && strings.TrimSpace(arg) == "" {
			return r, errors.New("resume command needs an executable and arguments without control characters")
		}
	}
	if size > 8<<10 {
		return r, errors.New("resume command exceeds 8 KiB")
	}
	return r, nil
}
