package proto

import (
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// AgentExplain asks for read-only detection evidence for Pane.
type AgentExplain struct {
	Pane     string
	Provider model.Provider // used only when the pane has no known provider
}

// AgentExplanation is schema version 1 of agent explain's JSON output.
type AgentExplanation struct {
	SchemaVersion int               `json:"schema_version"`
	Pane          string            `json:"pane"`
	Provider      model.Provider    `json:"provider"`
	State         model.AgentState  `json:"state"`
	Rule          string            `json:"rule"`
	Screen        agent.ScreenTrace `json:"screen"`
	Process       *AgentProcess     `json:"process"`
	Hook          *AgentHook        `json:"hook"`
}

// AgentProcess contains names only, never arguments or environment values.
type AgentProcess struct {
	Foreground int            `json:"foreground"`
	Provider   model.Provider `json:"provider"`
	Comm       string         `json:"comm"`
}

// AgentHook is the last state derived from a hook, even after its agent exits.
type AgentHook struct {
	Provider model.Provider   `json:"provider"`
	State    model.AgentState `json:"state"`
	At       time.Time        `json:"at"`
	AgeMS    int64            `json:"age_ms"`
}
