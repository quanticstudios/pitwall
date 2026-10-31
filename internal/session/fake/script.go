// Package fake provides scripted managed sessions without starting agent processes.
// Use New directly in tests, or ProvidersFromEnv at the daemon's provider registry.
package fake

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

//go:embed testdata/*.json
var scripts embed.FS

// Script has aide's fixture envelope. An empty Mode works with both providers.
// IDs in events are local to a turn and are prefixed on replay. ${provider}
// and ${prompt} in string values are replaced before replay.
type Script struct {
	Mode          model.Provider `json:"mode"`
	Prompt        string         `json:"prompt"`
	RuntimeEvents []Step         `json:"runtimeEvents"`
}

// Step is a runtime event with an optional delay and response gate. A request
// carrying Answers pauses replay until Approve or Answer supplies a response.
// Queue keeps replaying after such a request, so several stay open at once;
// replay then waits until every open request has a response.
// Repeat and Events compact large transcripts; ${index} is the zero-based
// repetition index. Plain aide runtime events need none of these extensions.
type Step struct {
	session.Event
	DelayMS int             `json:"delayMs,omitempty"`
	Answers *Answers        `json:"answers,omitempty"`
	Queue   bool            `json:"queue,omitempty"`
	Repeat  int             `json:"repeat,omitempty"`
	Events  []session.Event `json:"events,omitempty"`
}

// Answers optionally checks a response. Empty Answers accepts any response.
type Answers struct {
	Allow *bool               `json:"allow,omitempty"`
	Input map[string][]string `json:"input,omitempty"`
}

// Names returns the bundled scenario names, sorted lexically.
func Names() []string {
	entries, _ := fs.ReadDir(scripts, "testdata")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	return names
}

// Load returns a fresh script. It can be reused by driver, eventlog and protocol
// tests, or replaced with a conformance fixture decoded into Script.
func Load(name string) (Script, error) {
	var s Script
	b, err := scripts.ReadFile("testdata/" + name + ".json")
	if err != nil {
		return s, fmt.Errorf("fake script %q: %w", name, err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("fake script %q: %w", name, err)
	}
	return s, nil
}

// Event has its own JSON marshaler, so preserve the script fields explicitly.
func (s Step) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(s.Event)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	for key, value := range map[string]any{"delayMs": s.DelayMS, "answers": s.Answers, "queue": s.Queue, "repeat": s.Repeat, "events": s.Events} {
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[key] = b
	}
	return json.Marshal(fields)
}
func (s *Step) UnmarshalJSON(b []byte) error {
	var meta struct {
		DelayMS int             `json:"delayMs"`
		Answers *Answers        `json:"answers"`
		Queue   bool            `json:"queue"`
		Repeat  int             `json:"repeat"`
		Events  []session.Event `json:"events"`
	}
	var event session.Event
	if err := json.Unmarshal(b, &event); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return err
	}
	*s = Step{Event: event, DelayMS: meta.DelayMS, Answers: meta.Answers, Queue: meta.Queue, Repeat: meta.Repeat, Events: meta.Events}
	return nil
}
