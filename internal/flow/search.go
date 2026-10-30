package flow

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TextMatch is one matching transcript line. Record is its JSONL record number;
// Snippet is a bounded excerpt around the query. Text is never logged or saved.
type TextMatch struct {
	Record  uint64
	Time    time.Time
	Snippet string
}

// Search reads the conversation text of a supported session file, including
// text outside the panel's truncated summaries. Returning false from emit stops
// the read. Only the size at open is read, so a growing file cannot prolong a
// query indefinitely. Invalid records and unavailable files are skipped without
// logging their contents or parse errors.
func Search(ctx context.Context, provider model.Provider, path, query string, emit func(TextMatch) bool) {
	searchFile(ctx, provider, path, query, false, emit)
}

func searchFile(ctx context.Context, provider model.Provider, path, query string, child bool, emit func(TextMatch) bool) bool {
	if query == "" || !Reads(provider) || ctx.Err() != nil {
		return ctx.Err() == nil
	}
	f, fi, err := openRegular(path)
	if err != nil {
		return true
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.NewSectionReader(f, 0, fi.Size()), 64<<10)
	// Use the panel's own discovery for the child transcripts it follows.
	var parent *session
	if !child && (provider == model.ProviderClaude || provider == model.ProviderCodex) {
		parent = newSession(provider, path, false, time.Time{})
	}
	fold := !strings.ContainsFunc(query, unicode.IsUpper)
	if fold {
		query = strings.ToLower(query)
	}
	for record := uint64(1); ctx.Err() == nil; record++ {
		line, err := searchRecord(ctx, r)
		if err != nil {
			break
		}
		if parent != nil {
			parent.parse(line)
		}
		text, ts := searchText(provider, line)
		for _, line := range strings.Split(text, "\n") {
			if ctx.Err() != nil {
				return false
			}
			if snippet := searchSnippet(line, query, fold); snippet != "" {
				if !emit(TextMatch{Record: record, Time: ts, Snippet: snippet}) {
					return false
				}
			}
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if parent != nil {
		parent.p.resolve(parent.b)
		seen := map[string]bool{path: true}
		for _, sub := range parent.b.subs {
			if sub.file != "" && !seen[sub.file] {
				seen[sub.file] = true
				if !searchFile(ctx, provider, sub.file, query, true, emit) {
					return false
				}
			}
		}
	}
	return true
}

// searchRecord skips an oversized record and resumes at the next newline,
// using the same record limit as the side panel. Cancellation is checked
// between bounded reads, including while discarding a large tool result.
func searchRecord(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	var line []byte
	skip := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := r.ReadSlice('\n')
		if !skip {
			if len(line)+len(part) > maxLine {
				line, skip = nil, true
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(part) > 0 {
			err = nil
		}
		return line, err
	}
}

func searchText(provider model.Provider, line []byte) (string, time.Time) {
	switch provider {
	case model.ProviderClaude:
		var e claudeEntry
		if json.Unmarshal(line, &e) == nil && (e.Type == "user" || e.Type == "assistant") && !e.IsMeta {
			return conversationText(e.Message.Content), parseTime(e.Timestamp)
		}
	case model.ProviderCodex:
		var e codexEntry
		if json.Unmarshal(line, &e) == nil && e.Type == "response_item" {
			switch e.Payload.Type {
			case "message", "agent_message":
				return codexText(e.Payload.Content), parseTime(e.Timestamp)
			case "function_call_output", "custom_tool_call_output":
				return searchJSONText(e.Payload.Output), parseTime(e.Timestamp)
			case "function_call":
				return searchJSONText(e.Payload.Arguments), parseTime(e.Timestamp)
			case "local_shell_call":
				return strings.Join(e.Payload.Action.Command, " "), parseTime(e.Timestamp)
			case "custom_tool_call":
				return e.Payload.Input, parseTime(e.Timestamp)
			}
		}
	case model.ProviderPi, model.ProviderOmp:
		var e piEntry
		if json.Unmarshal(line, &e) == nil && e.Type == "message" {
			ts := parseTime(e.Timestamp)
			if ts.IsZero() && e.Message.Timestamp > 0 {
				ts = time.UnixMilli(e.Message.Timestamp)
			}
			return conversationText(e.Message.Content), ts
		}
	case model.ProviderGemini:
		var e geminiMessage
		if json.Unmarshal(line, &e) == nil && (e.Type == "user" || e.Type == "gemini") {
			parts := []string{codexText(e.Content)}
			for _, c := range e.ToolCalls {
				parts = append(parts, searchJSONText(c.Args))
			}
			return strings.Join(parts, "\n"), parseTime(e.Timestamp)
		}
	}
	return "", time.Time{}
}

func conversationText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Input     json.RawMessage `json:"input"`
		Arguments json.RawMessage `json:"arguments"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "tool_result":
			parts = append(parts, conversationText(b.Content))
		case "tool_use":
			parts = append(parts, searchJSONText(b.Input))
		case "toolCall":
			parts = append(parts, searchJSONText(b.Arguments))
		}
	}
	return strings.Join(parts, "\n")
}

// Search tool text values without matching JSON field names or usage metadata.
func searchJSONText(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var parts []string
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case string:
			parts = append(parts, v)
		case []any:
			for _, item := range v {
				collect(item)
			}
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				collect(v[k])
			}
		}
	}
	collect(value)
	return strings.Join(parts, "\n")
}

func searchSnippet(text, query string, fold bool) string {
	s := text
	if fold {
		s = strings.ToLower(s)
	}
	i := strings.Index(s, query)
	if i < 0 {
		return ""
	}
	// Rune offsets survive case folding whose UTF-8 byte width changes.
	at := len([]rune(s[:i]))
	runes := []rune(text)
	start := max(0, at-60)
	end := min(len(runes), max(at+len([]rune(query)), start+180))
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, string(runes[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}
