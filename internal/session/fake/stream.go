package fake

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// StreamConfig matches aide's AIDE_FAKE_STREAM_* controls. A nil Options.Stream
// uses aide's defaults. ToolEvery <= 0 or an empty ToolMix disables tool calls.
type StreamConfig struct {
	TokensPerSecond float64
	Seconds         float64
	ToolEvery       int
	ToolMix         []string
}

func defaultStream() *StreamConfig {
	return &StreamConfig{TokensPerSecond: 50, Seconds: 150, ToolEvery: 150, ToolMix: []string{"Bash", "Edit", "Read"}}
}
func streamFromEnv() (*StreamConfig, error) {
	s := defaultStream()
	for name, target := range map[string]*float64{"AIDE_FAKE_STREAM_TOKENS_PER_SECOND": &s.TokensPerSecond, "AIDE_FAKE_STREAM_SECONDS": &s.Seconds} {
		if value, ok := os.LookupEnv(name); ok {
			n, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return nil, fmt.Errorf("fake stream: invalid %s", name)
			}
			*target = n
		}
	}
	if value, ok := os.LookupEnv("AIDE_FAKE_STREAM_TOOL_EVERY"); ok {
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("fake stream: invalid AIDE_FAKE_STREAM_TOOL_EVERY")
		}
		s.ToolEvery = n
	}
	if value, ok := os.LookupEnv("AIDE_FAKE_STREAM_TOOL_MIX"); ok {
		s.ToolMix = nil
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				s.ToolMix = append(s.ToolMix, name)
			}
		}
	}
	return s, nil
}

// The prose is aide's PERF_STREAM_TEXT; words arrive at the configured rate.
const perfText = "## Reading the change\n\nThe session hub fans each snapshot out to every pane, so a **busy transcript** costs one copy per window instead of one per pane. Here is the loop that matters:\n\n```ts\nfor (const listener of listeners) listener(snapshot)\n```\n\n- Unchanged items keep their identity across snapshots.\n- Memoized turns skip rendering when their items match.\n- Off-screen workspaces hold the latest snapshot until shown.\n\nNext I will check the `content-visibility` rules and measure the minimap again. "

func perfScript(s *StreamConfig) Script {
	if s == nil {
		s = defaultStream()
	}
	var steps []Step
	// Split after whitespace as aide does, preserving Markdown and newlines.
	var words []string
	start := 0
	for i, r := range perfText {
		if r == ' ' || r == '\n' || r == '\t' {
			words = append(words, perfText[start:i+1])
			start = i + 1
		}
	}
	if start < len(perfText) {
		words = append(words, perfText[start:])
	}
	total := int(math.Round(s.TokensPerSecond * s.Seconds))
	item, tools, previousMS := 0, 0, 0
	for sent := 0; sent < total; sent++ {
		dueMS := int(math.Ceil(float64(sent+1)*1000/s.TokensPerSecond/10) * 10)
		steps = append(steps, Step{DelayMS: dueMS - previousMS, Event: session.Event{Type: session.EventContent, ItemID: fmt.Sprintf("perf-text-%d", item), Kind: session.KindAssistantText, Text: words[sent%len(words)]}})
		previousMS = dueMS
		if s.ToolEvery <= 0 || len(s.ToolMix) == 0 || (sent+1)%s.ToolEvery != 0 {
			continue
		}
		name := s.ToolMix[tools%len(s.ToolMix)]
		id := fmt.Sprintf("perf-%s-%d", name, tools)
		tools++
		item++
		for _, status := range []string{"running", "completed"} {
			tool := &session.ToolCall{CallID: id, ToolName: name, Status: status, Input: []byte(`{"file_path":"README.md"}`)}
			if name == "Bash" {
				tool.Command = "bun run test"
				tool.Input = []byte(`{"command":"bun run test"}`)
			}
			if status == "completed" {
				zero := 0
				tool.ExitCode = &zero
				tool.Output = perfText[:400]
			}
			steps = append(steps, Step{Event: session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindToolCall, Tool: tool}})
		}
	}
	steps = append(steps, Step{Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnCompleted}})
	return Script{Prompt: "perf-stream", RuntimeEvents: steps}
}
