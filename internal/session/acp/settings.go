package acp

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// modeState is SessionModeState from session/new and session/load.
type modeState struct {
	CurrentModeID  string `json:"currentModeId"`
	AvailableModes []struct {
		ID string `json:"id"`
	} `json:"availableModes"`
}

// configOption is a SessionConfigOption. Only select options carry a value
// id; boolean options have no category pitwall sets.
type configOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
}

// rpcCall is one request Set sends.
type rpcCall struct {
	method string
	params map[string]any
}

// configure records what the agent advertised and returns the current
// mode and model as a session.settings event, or nil when neither is known.
func (s *stream) configure(modes *modeState, options []configOption) []session.Event {
	if modes != nil && len(modes.AvailableModes) > 0 {
		s.modes = true
		s.mode = modes.CurrentModeID
	}
	if options != nil {
		s.options, s.values = map[string]string{}, map[string]string{}
		for _, o := range options {
			var v string
			if o.Type != "select" || json.Unmarshal(o.CurrentValue, &v) != nil {
				continue
			}
			s.values[o.ID] = v
			if o.Category != "" && s.options[o.Category] == "" {
				s.options[o.Category] = o.ID
			}
		}
		if !s.modes && s.options["mode"] != "" {
			s.mode = s.values[s.options["mode"]]
		}
	}
	e := session.Event{Type: session.EventSettings, Mode: s.mode, Model: s.values[s.options["model"]]}
	if e.Mode == "" && e.Model == "" {
		return nil
	}
	return []session.Event{e}
}

// state is a session/update about the session rather than the turn, which
// counts outside a turn too. False for any other update.
func (s *stream) state(raw json.RawMessage) ([]session.Event, bool) {
	var u struct {
		SessionUpdate     string         `json:"sessionUpdate"`
		CurrentModeID     string         `json:"currentModeId"`
		ConfigOptions     []configOption `json:"configOptions"`
		AvailableCommands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Input       *struct {
				Hint string `json:"hint"`
			} `json:"input"`
		} `json:"availableCommands"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return nil, false
	}
	switch u.SessionUpdate {
	case "available_commands_update":
		cmds := make([]session.Command, 0, len(u.AvailableCommands))
		for _, c := range u.AvailableCommands {
			cmd := session.Command{Name: c.Name, Description: c.Description}
			if c.Input != nil {
				cmd.Hint = c.Input.Hint
			}
			cmds = append(cmds, cmd)
		}
		return []session.Event{{Type: session.EventCommands, Commands: cmds}}, true
	case "current_mode_update":
		if u.CurrentModeID == "" {
			return nil, true
		}
		s.mode = u.CurrentModeID
		return []session.Event{{Type: session.EventSettings, Mode: s.mode}}, true
	case "config_option_update":
		return s.configure(nil, u.ConfigOptions), true
	}
	return nil, false
}

// settings is the requests that change what set names, or ErrUnsupported
// when the agent advertised no way to change one of them. Values already
// current send nothing.
func (s *stream) settings(set session.Settings) ([]rpcCall, error) {
	var calls []rpcCall
	option := func(category, value string) error {
		id := s.options[category]
		if id == "" {
			return fmt.Errorf("ACP %s: %w", category, session.ErrUnsupported)
		}
		calls = append(calls, rpcCall{"session/set_config_option", map[string]any{"sessionId": s.id, "configId": id, "value": value}})
		return nil
	}
	if set.Mode != "" && set.Mode != s.mode {
		if s.modes {
			calls = append(calls, rpcCall{"session/set_mode", map[string]any{"sessionId": s.id, "modeId": set.Mode}})
		} else if err := option("mode", set.Mode); err != nil {
			return nil, err
		}
	}
	for _, c := range []struct{ category, value string }{{"model", set.Model}, {"thought_level", set.Effort}} {
		if c.value == "" || c.value == s.values[s.options[c.category]] {
			continue
		}
		if err := option(c.category, c.value); err != nil {
			return nil, err
		}
	}
	return calls, nil
}

// input is an open form elicitation: its JSON-RPC id, each field's type,
// per field each option's label to the value the schema wants back, and
// per question the free-text field its typed answer goes to.
type input struct {
	ID     json.RawMessage
	types  map[string]string
	values map[string]map[string]string
	other  map[string]string
}

type enumOption struct {
	Const       string `json:"const"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type property struct {
	Type        string       `json:"type"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Enum        []string     `json:"enum"`
	OneOf       []enumOption `json:"oneOf"`
	Items       struct {
		Enum  []string     `json:"enum"`
		AnyOf []enumOption `json:"anyOf"`
	} `json:"items"`
}

// elicit turns a form elicitation/create into a user-input request: one
// question per schema property, in schema order. claude-agent-acp sends
// AskUserQuestion this way, and agy its ask_question, each question
// followed by a free-text field (question_0_custom, q1_other) that becomes
// the question's own typed row rather than a question of its own.
func (d *driver) elicit(m rpcMessage) []session.Event {
	var p struct {
		Mode            string `json:"mode"`
		Message         string `json:"message"`
		SessionID       string `json:"sessionId"`
		RequestedSchema struct {
			Properties json.RawMessage `json:"properties"`
		} `json:"requestedSchema"`
	}
	keys, props, err := []string(nil), map[string]property{}, json.Unmarshal(m.Params, &p)
	if err == nil {
		keys, err = objectKeys(p.RequestedSchema.Properties)
	}
	if err == nil && len(keys) > 0 {
		err = json.Unmarshal(p.RequestedSchema.Properties, &props)
	}
	if err != nil || p.Mode != "form" || p.SessionID != d.s.id || d.s.turn == "" || len(keys) == 0 {
		d.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": rpcError{Code: -32602, Message: "No active turn or unsupported elicitation"}})
		return nil
	}
	if d.s.cancelled {
		d.reply(m.ID, map[string]string{"action": "cancel"})
		return nil
	}
	in := input{ID: m.ID, types: map[string]string{}, values: map[string]map[string]string{}, other: map[string]string{}}
	questions := make([]session.Question, 0, len(keys))
	for i, k := range keys {
		pr := props[k]
		in.types[k] = pr.Type
		if n := len(questions); n > 0 && len(questions[n-1].Options) > 0 && pr.Type == "string" && len(pr.Enum)+len(pr.OneOf) == 0 &&
			(k == questions[n-1].ID+"_other" || k == questions[n-1].ID+"_custom") {
			in.other[questions[n-1].ID] = k
			continue
		}
		q := session.Question{ID: k, Header: pr.Title, Prompt: pr.Description, Options: []session.PromptOption{}, Multi: pr.Type == "array"}
		if q.Prompt == "" && strings.HasSuffix(strings.TrimSpace(pr.Title), "?") { // agy's title is the question itself
			q.Prompt, q.Header = pr.Title, ""
		}
		if q.Prompt == "" && i == 0 {
			q.Prompt = p.Message
		}
		q.Prompt = cmp.Or(q.Prompt, pr.Title, k)
		enum, titled := pr.Enum, pr.OneOf
		if pr.Type == "array" {
			enum, titled = pr.Items.Enum, pr.Items.AnyOf
		}
		for _, o := range titled {
			q.Options = append(q.Options, session.PromptOption{ID: o.Const, Label: cmp.Or(o.Title, o.Const), Description: o.Description})
		}
		for _, v := range enum {
			q.Options = append(q.Options, session.PromptOption{ID: v, Label: v})
		}
		if pr.Type == "boolean" {
			q.Options = append(q.Options, session.PromptOption{ID: "true", Label: "Yes"}, session.PromptOption{ID: "false", Label: "No"})
		}
		in.values[k] = map[string]string{}
		for _, o := range q.Options {
			in.values[k][o.Label] = o.ID
		}
		questions = append(questions, q)
	}
	key := "acp-input-" + string(m.ID)
	d.s.inputs[key] = in
	return []session.Event{{Type: session.EventInputRequested, TurnID: d.s.turn, RequestID: key, Detail: p.Message, Questions: questions}}
}

// content is an elicitation's accepted content: each answered field in its
// schema type. Empty when nothing was answered.
func (in input) content(answers map[string][]string) map[string]any {
	answers = maps.Clone(answers)
	for q, field := range in.other { // typed text goes to the question's free-text field
		var picked []string
		for _, v := range answers[q] {
			if _, ok := in.option(q, v); ok {
				picked = append(picked, v)
			} else if v != "" {
				answers[field] = append(slices.Clone(answers[field]), v)
			}
		}
		answers[q] = picked
	}
	content := map[string]any{}
	for k, typ := range in.types {
		var a []string
		for _, v := range answers[k] {
			if id, ok := in.option(k, v); ok {
				v = id // the card answers with an option's label
			}
			if v != "" {
				a = append(a, v)
			}
		}
		if len(a) == 0 {
			continue
		}
		content[k] = a[0]
		switch typ {
		case "array":
			content[k] = a
		case "boolean":
			content[k] = a[0] == "true"
		case "number", "integer":
			if _, err := strconv.ParseFloat(a[0], 64); err == nil {
				content[k] = json.Number(a[0])
			}
		}
	}
	return content
}

// option is the schema value of field k's option v, by its label or its
// value; false when v is no option, as typed text.
func (in input) option(k, v string) (string, bool) {
	if id, ok := in.values[k][v]; ok {
		return id, true
	}
	for _, id := range in.values[k] {
		if id == v {
			return id, true
		}
	}
	return "", false
}

// objectKeys is the keys of the JSON object b in their order, which a map
// loses.
func objectKeys(b json.RawMessage) ([]string, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("ACP schema properties are not an object")
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
