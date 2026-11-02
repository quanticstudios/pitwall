package daemon

import (
	"fmt"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// recordExplanation keeps the evidence at the point a detector sets state.
// Callers hold d.mu.
func (d *Daemon) recordExplanation(id string, p model.Provider, s model.AgentState, rule string, screen agent.ScreenTrace) {
	if d.live.explanations == nil {
		d.live.explanations = map[string]proto.AgentExplanation{}
	}
	d.live.explanations[id] = proto.AgentExplanation{Provider: p, State: s, Rule: rule, Screen: screen}
}

func (d *Daemon) agentExplain(m proto.AgentExplain) any {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.panes[m.Pane]
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if p == nil || i < 0 {
		return proto.Error{Message: fmt.Sprintf("no running pane %s", m.Pane)}
	}
	if m.Provider != "" && !model.IsAgent(m.Provider) {
		return proto.Error{Message: fmt.Sprintf("unknown provider %q", m.Provider)}
	}
	r := d.live.explanations[m.Pane]
	provider, state := d.st.Panes[i].Provider, model.AgentState("")
	if ai := d.activityIndex(m.Pane); ai >= 0 {
		provider, state = d.st.Activities[ai].Provider, d.st.Activities[ai].State
	}
	// Other activity sources, such as OSC notifications, own their state.
	if r.Rule == "" || r.State != state || r.Provider != provider {
		r.Rule = "daemon-activity"
		r.Screen = agent.ScreenTrace{}
	}
	r.SchemaVersion, r.Pane, r.Provider, r.State = 1, m.Pane, provider, state
	if f, ok := p.(foregrounder); ok {
		pg := f.Foreground()
		process := &proto.AgentProcess{Foreground: pg}
		if pg > 0 {
			process.Provider, process.Comm = identify(pg)
		}
		r.Process = process
	}
	if h, ok := d.live.lastHook[m.Pane]; ok {
		h.AgeMS = max(0, time.Since(h.At).Milliseconds())
		r.Hook = &h
	}
	if r.Screen.Checks == nil {
		screenProvider := provider
		if !model.IsAgent(screenProvider) && r.Process != nil {
			screenProvider = r.Process.Provider
		}
		if !model.IsAgent(screenProvider) {
			screenProvider = m.Provider
		}
		r.Screen = agent.ExplainScreen(screenProvider, p.Snapshot())
	}
	return r
}
