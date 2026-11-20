package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Claude Code's /usage, /cost and /status print a dialog in the TUI that
// leaves nothing in the transcript, and in stream-json /usage and /cost
// answer with a synthetic reply whose text leaves out the session's
// tokens. The functions here write what a chat should show for them in
// /cost's own words, which the chat's cards read and which reads fine as
// plain text when they cannot.

// localRun is a stream-json synthetic reply's local_command_run.
type localRun struct {
	Command string `json:"command"`
	Args    string `json:"args"`
}

// usageReport is a stream-json synthetic reply's usage_report: the
// session's cost and tokens by model.
type usageReport struct {
	Session struct {
		Cost   *float64 `json:"total_cost_usd"`
		Models map[string]struct {
			Input      int64    `json:"inputTokens"`
			Output     int64    `json:"outputTokens"`
			CacheRead  int64    `json:"cacheReadInputTokens"`
			CacheWrite int64    `json:"cacheCreationInputTokens"`
			Cost       *float64 `json:"costUSD"`
		} `json:"model_usage"`
	} `json:"session"`
}

// sessionLines is a session's cost and tokens by model as /cost prints
// them; "" without tokens. costs has each priced model's cost, cost the
// total, nil when unknown.
func sessionLines(cost *float64, models map[string]flow.Tokens, costs map[string]float64) string {
	if len(models) == 0 {
		return ""
	}
	var b strings.Builder
	if cost != nil {
		fmt.Fprintf(&b, "Estimated cost:  %s\n", dollars(*cost))
	}
	b.WriteString("Usage by model:")
	for _, m := range slices.Sorted(maps.Keys(models)) {
		t := models[m]
		fmt.Fprintf(&b, "\n%s:  %s input, %s output, %s cache read, %s cache write", m, kilo(t.Input), kilo(t.Output), kilo(t.CacheRead), kilo(t.CacheWrite))
		if c, ok := costs[m]; ok {
			fmt.Fprintf(&b, " (%s)", dollars(c))
		}
	}
	return b.String()
}

// reportLines is sessionLines of a stream-json usage_report.
func reportLines(raw json.RawMessage) string {
	var r usageReport
	if len(raw) == 0 || json.Unmarshal(raw, &r) != nil {
		return ""
	}
	models, costs := map[string]flow.Tokens{}, map[string]float64{}
	for name, m := range r.Session.Models {
		models[name] = flow.Tokens{Input: m.Input, Output: m.Output, CacheRead: m.CacheRead, CacheWrite: m.CacheWrite}
		if m.Cost != nil {
			costs[name] = *m.Cost
		}
	}
	return sessionLines(r.Session.Cost, models, costs)
}

// limitLines are l's windows at at, as /usage prints them: "Current
// week (all models): 52% used · resets Nov 24, 9pm".
func limitLines(l flow.Limit, at time.Time) string {
	var out []string
	for _, w := range l.Windows {
		w = w.Now(at)
		name := fmt.Sprintf("%dh window", w.Minutes/60)
		switch w.Minutes {
		case 5 * 60:
			name = "Current session"
		case 7 * 24 * 60:
			name = "Current week (all models)"
		}
		s := fmt.Sprintf("%s: %s%% used", name, trimZero(fmt.Sprintf("%.1f", w.Used)))
		if !w.Resets.IsZero() {
			r := w.Resets.Local()
			f := "Jan 2, 3:04pm"
			if r.Minute() == 0 {
				f = "Jan 2, 3pm"
			}
			s += " · resets " + r.Format(f)
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

// statusInfo is what /status shows that a chat knows.
type statusInfo struct {
	version, session, cwd, branch, model, mode string
}

// lines is s as /status prints it, a row per field known.
func (s statusInfo) lines() string {
	var out []string
	for _, kv := range [][2]string{{"Version", s.version}, {"Session ID", s.session}, {"cwd", s.cwd},
		{"Branch", s.branch}, {"Model", s.model}, {"Permission mode", s.mode}} {
		if kv[1] != "" {
			out = append(out, kv[0]+":  "+kv[1])
		}
	}
	return strings.Join(out, "\n")
}

// kilo is a token count as Claude Code prints it: 812, 28.2k, 1.4m.
func kilo(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "m"
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// dollars is a cost as /cost prints it: four places under a dollar.
func dollars(c float64) string {
	if c < 1 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

// joinParts joins the non-empty parts with a blank line.
func joinParts(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return strings.TrimSpace(s) == "" }), "\n\n")
}

func addTokens(a, b flow.Tokens) flow.Tokens {
	return flow.Tokens{Input: a.Input + b.Input, Output: a.Output + b.Output, CacheRead: a.CacheRead + b.CacheRead, CacheWrite: a.CacheWrite + b.CacheWrite}
}

// limitAge is how far a limit report may be from a command and still
// show as its limits.
const limitAge = 15 * time.Minute

// note keeps what line l says about the session, for /status.
func (t *Transcript) note(l transcriptLine) {
	for _, f := range []struct {
		to *string
		v  string
	}{{&t.info.version, l.Version}, {&t.info.session, l.SessionID},
		{&t.info.cwd, l.Cwd}, {&t.info.branch, l.GitBranch}, {&t.info.mode, l.PermissionMode}} {
		if f.v != "" {
			*f.to = f.v
		}
	}
}

// Dialog is cmd's chip and output, for a chat that runs /usage, /cost or
// /status itself at at: in the TUI each opens a dialog and writes nothing
// to the transcript. nil for any other command, and when the transcript
// knows nothing to show.
func (t *Transcript) Dialog(cmd string, at time.Time) []session.Event {
	text := t.local(cmd, at)
	if text == "" {
		return nil
	}
	ts := session.Stamp(at)
	id := "dialog:" + ts
	chip := t.command(id, cmd)
	chip.CreatedAt = ts
	return []session.Event{chip, {Type: session.EventContent, ItemID: id + ":out", Kind: session.KindSystem,
		Phase: session.PhaseOutput, Text: text, CreatedAt: ts}}
}

// local is what the TUI's dialog for /usage, /cost or /status run at at
// showed, as far as the transcript knows: "" for any other command.
func (t *Transcript) local(cmd string, at time.Time) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(cmd, "/"), " ")
	switch name {
	case "status":
		st := t.info
		st.model = t.model
		return st.lines()
	case "usage", "cost":
	default:
		return ""
	}
	models := maps.Clone(t.total)
	for _, id := range t.usedOrder {
		m := t.used[id]
		models[m.model] = addTokens(models[m.model], m.t)
	}
	costs := map[string]float64{}
	var total float64
	for name, k := range models {
		if c, ok := (flow.Usage{Models: map[string]flow.Tokens{name: k}}).Cost(); ok {
			costs[name] = c
			total += c
		}
	}
	var cost *float64
	if len(costs) == len(models) {
		cost = &total
	}
	var lim string
	if t.Limit != nil {
		if l, ok := t.Limit(); ok && l.Seen.Sub(at).Abs() < limitAge {
			lim = limitLines(l, at)
		}
	}
	return joinParts(lim, sessionLines(cost, models, costs))
}
