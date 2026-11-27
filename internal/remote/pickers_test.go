package remote

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// pickerState is one tab per pane in panes, each its own provider.
func pickerState(panes ...model.Pane) model.State {
	st := model.State{Sessions: []model.Session{{ID: "s", Name: "main"}}}
	for _, p := range panes {
		p.WorkspaceID = "w" + p.ID
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: p.WorkspaceID, SessionID: "s", Tabs: []model.Tab{{Layout: &layout.Node{Pane: p.ID}}}})
		st.Panes = append(st.Panes, p)
	}
	return st
}

// TestTabPickers: each agent's tab lists what its chips offer, and an
// agent pitwall cannot switch lists none.
func TestTabPickers(t *testing.T) {
	st := pickerState(
		model.Pane{ID: "c", Provider: model.ProviderClaude, AgentMode: "plan"},
		model.Pane{ID: "b", Provider: model.ProviderClaude, AgentMode: "bypassPermissions"},
		model.Pane{ID: "g", Provider: model.ProviderAntigravity},
		model.Pane{ID: "x", Provider: model.ProviderCodex},
		model.Pane{ID: "e", Provider: model.ProviderClaude, AgentMode: "plan", Exited: true},
	)
	tabs := map[string]Tab{}
	for _, tb := range Tabs(st, nil) {
		tabs[tb.Pane] = tb
	}
	c := tabs["c"]
	if !c.Picker || !reflect.DeepEqual(c.Models, Models) || !reflect.DeepEqual(c.Efforts, Efforts) || c.Mode != "plan" ||
		!reflect.DeepEqual(c.Modes, []string{"default", "acceptEdits", "plan"}) {
		t.Errorf("claude %+v", c)
	}
	if b := tabs["b"]; b.Modes[len(b.Modes)-1] != "bypassPermissions" {
		t.Errorf("a session started in bypass: %v", b.Modes)
	}
	if g := tabs["g"]; g.Picker || g.Models != nil || !reflect.DeepEqual(g.Efforts, AgyEfforts) || !reflect.DeepEqual(g.Modes, AgyModes) {
		t.Errorf("agy %+v", g)
	}
	if x := tabs["x"]; !x.Send || x.Models != nil || x.Efforts != nil || x.Modes != nil {
		t.Errorf("codex %+v", x)
	}
	if e := tabs["e"]; e.Models != nil || e.Modes != nil || e.Mode != "" {
		t.Errorf("exited %+v", e)
	}
	b, _ := json.Marshal(tabs["x"])
	if strings.Contains(string(b), "models") || strings.Contains(string(b), "modes") {
		t.Errorf("codex wire %s", b)
	}
}

// TestCommandOffered: /api/command takes only what the pane's tab offers,
// a mode included, and passes it on as a slash command.
func TestCommandOffered(t *testing.T) {
	s, h := newServer(t)
	tabs := Tabs(pickerState(model.Pane{ID: "c", Provider: model.ProviderClaude}, model.Pane{ID: "g", Provider: model.ProviderAntigravity}, model.Pane{ID: "x", Provider: model.ProviderCodex}), nil)
	tabs[1].Models = []string{"gemini-4-neon"}
	s.Tabs = func() []Tab { return tabs }
	var sent []string
	s.Command = func(pane, command string) error { sent = append(sent, pane+" "+command); return nil }
	h = s.Handler()
	token := pairDevice(t, s, h, "pixel")
	for _, tc := range []struct {
		pane, what, value string
		want              int
	}{
		{"c", "model", "opus", 200},
		{"c", "mode", "acceptEdits", 200},
		{"g", "model", "gemini-4-neon", 200},
		{"g", "effort", "xhigh", 200},
		{"g", "mode", "accept-edits", 200},
		{"g", "model", "opus", 400},        // Claude's alias, not agy's
		{"g", "effort", "max", 400},        // agy has no max
		{"c", "mode", "accept-edits", 400}, // agy's word, not Claude's
		{"x", "mode", "default", 400},      // Codex: no mode from here
		{"nope", "model", "opus", 400},
		{"c", "clear", "", 400},
	} {
		b, _ := json.Marshal(map[string]string{"pane": tc.pane, "what": tc.what, "value": tc.value})
		if st, body := call(t, h, "POST", "/api/command", token, string(b)); st != tc.want {
			t.Errorf("%s %s %q: %d %s", tc.pane, tc.what, tc.value, st, body)
		}
	}
	want := []string{"c /model opus", "c /mode acceptEdits", "g /model gemini-4-neon", "g /effort xhigh", "g /mode accept-edits"}
	if !reflect.DeepEqual(sent, want) {
		t.Errorf("sent %q", sent)
	}
}
