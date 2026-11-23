package agent

import (
	"os"
	"testing"
)

// The captured files are Claude Code 2.1.330's own statusLine JSON: one
// before the session's first reply and one after it.
func TestParseStatuslineCaptured(t *testing.T) {
	cases := []struct {
		file string
		want StatuslineState
	}{
		{"statusline-claude-2.1.330-start.json", StatuslineState{SessionID: "e60facbc-9917-4165-9576-3ec629c1da08", Transcript: "/cfg/projects/-work/e60facbc-9917-4165-9576-3ec629c1da08.jsonl",
			Model: "claude-sonnet-5-5", ModelName: "Sonnet 5.5", Effort: "low", ContextSize: 1_000_000, Version: "2.1.330"}},
		{"statusline-claude-2.1.330-reply.json", StatuslineState{SessionID: "e60facbc-9917-4165-9576-3ec629c1da08", Transcript: "/cfg/projects/-work/e60facbc-9917-4165-9576-3ec629c1da08.jsonl",
			Model: "claude-sonnet-5-5", ModelName: "Sonnet 5.5", Effort: "low", ContextSize: 1_000_000, ContextUsed: 2 + 13787 + 28195, CostUSD: 0.0580115, Version: "2.1.330"}},
	}
	for _, c := range cases {
		data, err := os.ReadFile("testdata/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ParseStatusline(data)
		if !ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v", c.file, got, ok, c.want)
		}
		back, ok := DecodeStatusline(EncodeStatusline(got))
		if !ok || back != got {
			t.Errorf("%s: round trip gave %+v %v", c.file, back, ok)
		}
	}
}

func TestParseStatuslineEdges(t *testing.T) {
	for _, in := range []string{``, `[]`, `{}`, `{"model":{}}`, `{"rate_limits":{}}`} {
		if _, ok := ParseStatusline([]byte(in)); ok {
			t.Errorf("%q parsed", in)
		}
	}
	// Older Claude Code: a model with no effort, window or usage.
	if st, ok := ParseStatusline([]byte(`{"model":{"display_name":"Opus"},"cost":{"total_cost_usd":-1},"context_window":{"context_window_size":-5}}`)); !ok || st != (StatuslineState{ModelName: "Opus"}) {
		t.Errorf("sparse JSON gave %+v %v", st, ok)
	}
	// A report-agent payload, with or without a name, is not a statusline.
	for _, in := range []string{`{"name":"x","statusline":{"model":"m"}}`, `{"program":"p"}`, `{"statusline":{}}`, `nope`} {
		if _, ok := DecodeStatusline([]byte(in)); ok {
			t.Errorf("%q decoded", in)
		}
	}
	if _, err := DecodeReport(EncodeStatusline(StatuslineState{Model: "m"})); err == nil {
		t.Error("an older daemon would accept a statusline as an agent report")
	}
}
