package settings

import (
	"context"
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// decisionsPage is the Decisions category's state.
type decisionsPage struct {
	key    widget.Editor // the API key being pasted, masked
	keySrc string        // where the saved key comes from, "" for none
	keyErr string        // why the key cannot be used, or why connecting failed
	info   model.DecideInfo

	mu      sync.Mutex
	testing bool
	result  string // the last test: "ok · 143 ms" or the error
	ok      bool
}

// ping makes the connection test's call; tests replace it.
var ping = decide.Ping

// SetDecisions gives the page the daemon's decision status to show.
func (p *Page) SetDecisions(info model.DecideInfo) { p.dp.info = info }

func (p *Page) credPath() string {
	path := p.s.Path
	if path == "" {
		path = config.Path()
	}
	return decide.CredentialsPath(filepath.Dir(path))
}

// readKey refreshes where the key comes from. The key itself is not kept.
func (p *Page) readKey() {
	_, src, err := decide.LoadKey(p.credPath())
	p.dp.keySrc, p.dp.keyErr = src, ""
	if err != nil {
		p.dp.keyErr = err.Error()
	}
}

// connect saves the pasted key and turns Jev on, then tests it.
func (p *Page) connect() {
	key := strings.TrimSpace(p.dp.key.Text())
	if err := decide.CheckKey(key); err != nil {
		p.dp.keyErr = err.Error()
		return
	}
	if err := decide.SaveKey(p.credPath(), key); err != nil {
		p.dp.keyErr = err.Error()
		return
	}
	p.dp.key.SetText("")
	p.readKey()
	if p.s.Decisions.Provider != "jev" {
		p.saveValue("decisions", "provider", config.Quote("jev"))
	}
	p.test()
}

// disconnect removes the saved key and turns decisions off.
func (p *Page) disconnect() {
	if err := decide.DeleteKey(p.credPath()); err != nil {
		p.dp.keyErr = err.Error()
		return
	}
	p.readKey()
	p.dp.mu.Lock()
	p.dp.result = ""
	p.dp.mu.Unlock()
	if p.s.Decisions.Provider != "" {
		p.saveValue("decisions", "provider", config.Quote(""))
	}
}

// test makes one real call in the background.
func (p *Page) test() {
	d := p.s.Decisions
	var prov decide.Provider
	var secrets []string
	if d.Provider == "command" {
		prov = decide.Command{Argv: d.Command}
	} else {
		key, _, err := decide.LoadKey(p.credPath())
		if err != nil || key == "" {
			p.dp.mu.Lock()
			p.dp.result, p.dp.ok = "No key to test.", false
			p.dp.mu.Unlock()
			return
		}
		prov, secrets = decide.NewJev(key, d.Model), []string{key}
	}
	p.dp.mu.Lock()
	if p.dp.testing {
		p.dp.mu.Unlock()
		return
	}
	p.dp.testing = true
	p.dp.mu.Unlock()
	go func() {
		took, err := ping(context.Background(), prov, 10*time.Second, secrets...)
		p.dp.mu.Lock()
		defer p.dp.mu.Unlock()
		p.dp.testing = false
		if err != nil {
			p.dp.result, p.dp.ok = err.Error(), false
		} else {
			p.dp.result, p.dp.ok = fmt.Sprintf("Connection ok · %d ms", took.Milliseconds()), true
		}
	}()
}

// sends says where a feature's data goes.
func (p *Page) sends(what string) string {
	switch p.s.Decisions.Provider {
	case "command":
		return "Sends " + what + " to your command, secrets removed."
	}
	return "Sends " + what + " to TypeSafe (api.typesafe.ai), secrets removed."
}

// counted is a feature's calls today, for its description.
func (p *Page) counted(feature string) string {
	for _, c := range p.dp.info.Counts {
		if c.Feature == feature {
			if c.Errors > 0 {
				return fmt.Sprintf(" Today: %d calls, %d failed.", c.Calls, c.Errors)
			}
			return fmt.Sprintf(" Today: %d calls.", c.Calls)
		}
	}
	return ""
}

func (p *Page) decisions() []section {
	th := p.th
	d := p.s.Decisions
	p.dp.mu.Lock()
	testing, result, ok := p.dp.testing, p.dp.result, p.dp.ok
	p.dp.mu.Unlock()
	status := func(gtx gl.Context) gl.Dimensions {
		if testing {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
		}
		var kids []gl.Widget
		if p.dp.keyErr != "" {
			kids = append(kids, func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, th.UIFont, p.sp(12), th.Red, p.dp.keyErr)
			})
		}
		switch {
		case testing:
			kids = append(kids, func(gtx gl.Context) gl.Dimensions { return p.para(gtx, th.UIFont, p.sp(12), th.Muted, "Testing…") })
		case result != "":
			col := th.Red
			if ok {
				col = th.Green
			}
			kids = append(kids, func(gtx gl.Context) gl.Dimensions { return p.para(gtx, th.UIFont, p.sp(12), col, result) })
		}
		if len(kids) == 0 {
			return gl.Dimensions{}
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, func() []gl.FlexChild {
			var out []gl.FlexChild
			for _, k := range kids {
				out = append(out, gl.Rigid(k))
			}
			return out
		}()...)
	}
	link := func(id, label, url string) gl.Widget {
		return func(gtx gl.Context) gl.Dimensions {
			c := p.btn(id)
			for c.Clicked(gtx) {
				open(url)
			}
			return p.button(gtx, c, ghost, label)
		}
	}
	btn := func(id, label string, kind btnKind, click func()) gl.Widget {
		return func(gtx gl.Context) gl.Dimensions {
			c := p.btn(id)
			for c.Clicked(gtx) {
				click()
			}
			return p.button(gtx, c, kind, label)
		}
	}

	var conn row
	switch {
	case d.Provider == "command":
		argv := ""
		if len(d.Command) > 0 {
			argv = strings.Join(d.Command, " ")
		}
		conn = row{label: "Your command", desc: "provider = \"command\" in config.toml runs " + argv + " for every question.",
			extra: "decisions provider command local model", control: btn("dtest", "Test", secondary, p.test), below: status}
	case p.dp.keySrc != "" && d.Provider == "jev":
		where := "the credentials file, " + shortPath(p.credPath()) + " (mode 0600)"
		if p.dp.keySrc == decide.FromEnv {
			where = "TYPESAFE_API_KEY in pitwall's environment"
		}
		conn = row{label: "Jev is connected", desc: "The key comes from " + where + ". Model " + d.Model + ".",
			extra: "jev typesafe key api connect disconnect test",
			control: func(gtx gl.Context) gl.Dimensions {
				ws := []gl.Widget{btn("dtest", "Test connection", secondary, p.test)}
				if p.dp.keySrc == decide.FromFile {
					ws = append(ws, btn("dlogout", "Disconnect", danger, p.disconnect))
				} else {
					ws = append(ws, btn("doff", "Turn off", secondary, func() { p.saveValue("decisions", "provider", config.Quote("")) }))
				}
				return hstack(gtx, 8, ws...)
			}, below: status}
	case p.dp.keySrc != "":
		conn = row{label: "Connect Jev", desc: "A key is saved, but decisions are off.", extra: "jev typesafe key api connect",
			control: btn("don", "Turn on", primary, func() { p.saveValue("decisions", "provider", config.Quote("jev")); p.test() }), below: status}
	default:
		conn = row{label: "Connect Jev",
			desc: "TypeSafe's Jev answers yes/no and multiple-choice questions in about a tenth of a second, with calibrated probabilities. " +
				"Paste your API key; pitwall keeps it in " + shortPath(p.credPath()) + " with mode 0600 (on Windows, your profile folder's permissions), never in config.toml. " +
				"Nothing is sent until you connect.",
			extra: "jev typesafe key api connect token", wide: true,
			control: func(gtx gl.Context) gl.Dimensions {
				p.dp.key.SingleLine, p.dp.key.Submit, p.dp.key.Mask = true, true, '•'
				for {
					ev, ok := p.dp.key.Update(gtx)
					if !ok {
						break
					}
					if _, ok := ev.(widget.SubmitEvent); ok {
						p.connect()
					}
				}
				return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
					gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
						return p.field(gtx, &p.dp.key, "Paste your TypeSafe API key", false)
					}),
					gl.Rigid(gl.Spacer{Width: 8}.Layout),
					gl.Rigid(btn("dconnect", "Connect", primary, p.connect)),
					gl.Rigid(gl.Spacer{Width: 4}.Layout),
					gl.Rigid(link("dkeys", "Get a key", decide.KeysURL)),
				)
			}, below: status}
	}

	featDesc := "Connect a provider to turn these on. Until then nothing is sent anywhere."
	if d.On() {
		featDesc = "Each feature asks only when it has something to ask. A failed or slow answer changes nothing: pitwall goes on as it would without one."
	}
	rows := []row{
		{label: "Approvals", desc: "When Claude Code or Codex asks permission. Suggest shows a recommendation on the approval. Auto fails closed: it approves only calls pitwall can fully check " +
			"(plain shell words, file writes inside the repo) that break no hard rule, denies only sure denies, and leaves everything else to you. " +
			p.sends("the tool, its input, the folder and your latest prompt") + p.counted(decide.FeatureApprovals),
			extra: "approval permission auto suggest allow deny mode",
			control: p.segmented("dmode", []string{config.ModeOff, config.ModeSuggest, config.ModeAuto}, d.Approvals, func(o string) {
				p.saveValue("decisions.approvals", "mode", config.Quote(o))
			})},
	}
	if d.Approvals == config.ModeAuto {
		rows = append(rows,
			row{label: "Approve above", desc: "Auto mode approves when the probability of allow is at least this.", extra: "threshold allow_above",
				control: p.stepper("decisions.approvals", "allow_above", d.AllowAbove, 0.8, 1, 0.01, config.DefaultAllowAbove)},
			row{label: "Deny above", desc: "Auto mode denies when the probability of deny is at least this.", extra: "threshold deny_above",
				control: p.stepper("decisions.approvals", "deny_above", d.DenyAbove, 0.8, 1, 0.01, config.DefaultDenyAbove)})
	}
	rows = append(rows,
		row{label: "Attention triage", desc: "Rates how soon a pane that needs you wants you: fyi, later, soon or now. The jump-to-attention key goes to the most urgent first; fyi sends no desktop notification. " +
			p.sends("the agent's question, approval, error or summary") + p.counted(decide.FeatureTriage),
			extra: "triage urgency notification attention", control: p.toggle("decisions.triage", "enabled", d.Triage)},
		row{label: "Agents without hooks", desc: "Status for Gemini CLI, OpenCode, Aider, Amp, Cursor agent, Goose and Crush, read from their screen at most every 2 seconds while it changes. No other program's screen is ever sent. " +
			p.sends("the visible screen of those programs") + p.counted(decide.FeatureAgents),
			extra: "gemini opencode aider amp cursor goose crush hookless screen", control: p.toggle("decisions.agents", "enabled", d.Agents)},
		row{label: "Turn check", desc: "When an agent finishes, asks whether the turn needs your review: failed tests, errors left, unfinished work. The Done pill then reads Check. " +
			p.sends("the agent's last message and its screen") + p.counted(decide.FeatureTurnCheck),
			extra: "review done check finished turn", control: p.toggle("decisions.turn_check", "enabled", d.TurnCheck)},
	)

	secs := []section{{rows: []row{conn}}, {title: "Features", desc: featDesc, rows: rows}}
	if a := p.dp.info.Audit; len(a) > 0 || d.Approvals == config.ModeAuto {
		secs = append(secs, section{title: "Automatic decisions", desc: "What pitwall approved or denied for you since the daemon started, newest first. A tab's menu can stop automatic approvals for that tab.",
			rows: p.auditRows()})
	}
	return secs
}

func (p *Page) auditRows() []row {
	th := p.th
	a := slices.Clone(p.dp.info.Audit)
	slices.Reverse(a)
	if len(a) == 0 {
		return []row{{label: "None yet", desc: "Approvals pitwall answers show here.", extra: "audit"}}
	}
	var rows []row
	for i, e := range a {
		if i == 20 {
			rows = append(rows, row{label: fmt.Sprintf("and %d earlier", len(a)-20), extra: "audit"})
			break
		}
		verb := "Allowed"
		col := th.Green
		if e.Verdict == decide.Deny {
			verb, col = "Denied", th.Red
		}
		desc := fmt.Sprintf("%s · %s · %s · allow %.0f%%, ask %.0f%%, deny %.0f%%", e.At.Format("15:04:05"), e.Tab, e.Tool, e.Allow*100, e.Ask*100, e.Deny*100)
		if len(e.Rules) > 0 {
			desc += " · rules: " + strings.Join(e.Rules, ", ")
		}
		rows = append(rows, row{label: e.Input, desc: desc, extra: "audit " + e.Tool + " " + e.Tab,
			control: func(gtx gl.Context) gl.Dimensions {
				return boxed(gtx, theme.Mix(th.SurfaceSecondary, col, 0.12), theme.Mix(th.SurfaceSecondary, col, 0.12), gtx.Dp(4), image.Pt(gtx.Dp(6), gtx.Dp(2)), func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, th.UIFont, p.sp(12), col, verb)
				})
			}})
	}
	return rows
}
