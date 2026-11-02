package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const agentUsage = "usage: pitwall agent explain [-s session] <pane> [--provider name] [--json], or pitwall agent explain --file path [--provider name] [--json]"

func agentCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "explain" {
		return errors.New(agentUsage)
	}
	flags := flag.NewFlagSet("agent explain", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "")
	provider := flags.String("provider", "", "")
	asJSON := flags.Bool("json", false, "")
	sessionName := flags.String("s", "", "")
	args, err := parseFlags(flags, args[1:])
	if err != nil || *file == "" && len(args) != 1 || *file != "" && (len(args) != 0 || *sessionName != "") {
		return errors.New(agentUsage)
	}
	p := model.Provider(*provider)
	if p != "" && !model.IsAgent(p) {
		return fmt.Errorf("unknown provider %q", p)
	}
	var r proto.AgentExplanation
	if *file != "" {
		if p == "" {
			name := strings.TrimSuffix(filepath.Base(*file), filepath.Ext(*file))
			name = strings.TrimPrefix(name, "tuios-")
			p = agent.Identify(name, "")
			if p == "" {
				name, _, _ = strings.Cut(name, "-")
				p = agent.Identify(name, "")
			}
		}
		if p == "" {
			return errors.New("cannot infer provider from file name; pass --provider")
		}
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(strings.TrimRight(string(b), "\r\n"), "\r\n", "\n")
		lines := strings.Split(text, "\n")
		cols := 1
		for _, l := range lines {
			cols = max(cols, len(l))
		}
		e := vt.New(cols, max(1, len(lines)), nil)
		e.Write([]byte(strings.ReplaceAll(text, "\n", "\r\n")))
		trace := agent.ExplainScreen(p, e.Snapshot())
		r = proto.AgentExplanation{SchemaVersion: 1, Provider: p, State: trace.State, Rule: trace.Rule, Screen: trace}
	} else {
		r, err = explainPane(args[0], *sessionName, p)
		if err != nil {
			return err
		}
	}
	// gob decodes an empty slice as nil; JSON keeps checks as an array.
	if r.Screen.Checks == nil {
		r.Screen.Checks = []agent.ScreenCheck{}
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(r)
	}
	return printExplanation(out, r)
}

func explainPane(address, sessionName string, provider model.Provider) (proto.AgentExplanation, error) {
	conn, err := dialCLI()
	if err != nil {
		return proto.AgentExplanation{}, err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return proto.AgentExplanation{}, err
	}
	session, err := currentSession(m.State, sessionName)
	if err != nil {
		return proto.AgentExplanation{}, err
	}
	_, p, err := resolvePane(m.State, session.ID, address)
	if err != nil {
		return proto.AgentExplanation{}, err
	}
	if err := conn.Send(proto.AgentExplain{Pane: p.ID, Provider: provider}); err != nil {
		return proto.AgentExplanation{}, err
	}
	for {
		msg, err := conn.Recv()
		if err != nil {
			return proto.AgentExplanation{}, err
		}
		switch r := msg.(type) {
		case proto.AgentExplanation:
			return r, nil
		case proto.Error:
			return proto.AgentExplanation{}, errors.New(r.Message)
		}
	}
}

func printExplanation(out io.Writer, r proto.AgentExplanation) error {
	var b strings.Builder
	fmt.Fprintf(&b, "provider: %s\nstate: %s\nrule: %s\n", r.Provider, explainState(r.State), r.Rule)
	if r.Process != nil {
		fmt.Fprintf(&b, "process: foreground=%d comm=%q provider=%q\n", r.Process.Foreground, r.Process.Comm, r.Process.Provider)
	}
	if r.Pane != "" {
		if r.Hook == nil {
			b.WriteString("hook: none\n")
		} else {
			fmt.Fprintf(&b, "hook: provider=%s state=%s age=%dms\n", r.Hook.Provider, explainState(r.Hook.State), r.Hook.AgeMS)
		}
	}
	fmt.Fprintf(&b, "screen: provider=%s state=%s rule=%s\n", r.Screen.Provider, explainState(r.Screen.State), r.Screen.Rule)
	for _, c := range r.Screen.Checks {
		if c.Matched {
			fmt.Fprintf(&b, "  matched %s pattern=%q line=%q\n", c.Name, c.Pattern, c.Line)
		} else {
			fmt.Fprintf(&b, "  no match %s pattern=%q\n", c.Name, c.Pattern)
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func explainState(s model.AgentState) string {
	if s == "" {
		return "none"
	}
	return string(s)
}
