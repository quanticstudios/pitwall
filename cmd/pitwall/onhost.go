package main

import (
	"errors"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// onHost is the [[hosts]] machine whose daemon this CLI call talks to, as
// --host on ls, read, send, prompt, wait, kill and session ls names it; ""
// for this machine. dialKind dials it over ssh as box:3 addresses do.
var onHost string

// hostDones drop the forwards dialKind opened to onHost; runCLI runs them.
var hostDones []func()

// hostCommand reports whether args are a command that takes --host to
// run on another machine. new takes its own --host, which opens a tab there in a session
// here.
func hostCommand(args []string) bool {
	switch {
	case len(args) == 0:
		return false
	case args[0] == "session":
		return len(args) > 1 && args[1] == "ls"
	}
	return slices.Contains([]string{"ls", "read", "send", "prompt", "wait", "kill"}, args[0])
}

// cutHost takes --host <name> or --host=<name> out of args before any
// "--", and returns args without it and the name.
func cutHost(args []string) ([]string, string, error) {
	var out []string
	host := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		// These flags consume the next word even when it looks like
		// --host. Leave their values for the command's own flag parser.
		if (a == "-s" || a == "--s") && args[0] != "session" ||
			(a == "-until" || a == "--until") && args[0] == "wait" ||
			(a == "-timeout" || a == "--timeout") && (args[0] == "wait" || args[0] == "prompt") {
			out = append(out, a)
			if i+1 < len(args) && args[i+1] != "--" {
				i++
				out = append(out, args[i])
			}
			continue
		}
		name, ok := "", false
		switch {
		case a == "--host" || a == "-host":
			if i+1 < len(args) {
				name, i = args[i+1], i+1
			}
			ok = true
		case strings.HasPrefix(a, "--host=") || strings.HasPrefix(a, "-host="):
			_, name, ok = strings.Cut(a, "=")
		}
		if !ok {
			out = append(out, a)
			continue
		}
		if host != "" || name == "" || strings.HasPrefix(name, "-") || name == "local" {
			return nil, "", errors.New("--host takes one [[hosts]] name other than local")
		}
		host = name
	}
	return out, host, nil
}

// hostLocal is addr as onHost's daemon numbers it: box:3, #box:3 and
// box:@id, the addresses ls --host box prints, are 3 and @id there.
func hostLocal(addr string) string {
	if onHost == "" {
		return addr
	}
	if rest, ok := strings.CutPrefix(strings.TrimPrefix(addr, "#"), onHost+":"); ok {
		return rest
	}
	return addr
}

// hostWhere names each tab of session in state as on onHost, for the
// rows of ls --host.
func hostWhere(state model.State, session string) func(model.Workspace) (string, int) {
	if onHost == "" {
		return nil
	}
	tabs := state.Numbered(session)
	return func(w model.Workspace) (string, int) {
		return onHost, slices.IndexFunc(tabs, func(x model.Workspace) bool { return x.ID == w.ID }) + 1
	}
}

// errHostChildren refuses what lists or waits on the calling pane's
// workers on another machine: they belong to this machine's tab.
var errHostChildren = errors.New("--children and --orphans follow the calling pane's tab here and take no --host; run them without it")
