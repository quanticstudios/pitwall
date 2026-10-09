package main

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// commands reads the commands and their subcommands off usage, so a
// completion offers what usage lists: "  pitwall <cmd> <sub>" makes sub a
// subcommand of cmd, and "  pitwall <cmd> <placeholder>  a, b, c: text"
// makes a, b and c ones.
func commands() (top []string, subs map[string][]string) {
	subs = map[string][]string{}
	add := func(list []string, w string) []string {
		if slices.Contains(list, w) {
			return list
		}
		return append(list, w)
	}
	for _, line := range strings.Split(usage, "\n") {
		rest, ok := strings.CutPrefix(line, "  pitwall ")
		if !ok {
			continue
		}
		head, desc, _ := strings.Cut(rest, "  ")
		fields := strings.Fields(head)
		if len(fields) == 0 || !word.MatchString(fields[0]) && !strings.HasPrefix(fields[0], "-") {
			continue
		}
		name := fields[0]
		top = add(top, name)
		switch {
		case len(fields) > 1 && word.MatchString(fields[1]):
			subs[name] = add(subs[name], fields[1])
		case len(fields) == 2 && strings.HasPrefix(fields[1], "<"):
			list, _, _ := strings.Cut(desc, ":")
			list, _, _ = strings.Cut(list, "(")
			var ws []string
			for _, w := range strings.Split(list, ",") {
				if w = strings.TrimSpace(w); !word.MatchString(w) {
					ws = nil
					break
				}
				ws = append(ws, w)
			}
			for _, w := range ws {
				subs[name] = add(subs[name], w)
			}
		}
	}
	return top, subs
}

var word = regexp.MustCompile(`^[a-z][a-z-]*$`)

func runCompletion(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: pitwall completion bash|zsh|fish")
	}
	s, err := completion(args[0])
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, s)
	return err
}

// completion is shell's script completing pitwall's commands and
// subcommands, and files after them.
func completion(shell string) (string, error) {
	top, subs := commands()
	var b strings.Builder
	names := make([]string, 0, len(subs))
	for _, n := range top {
		if subs[n] != nil {
			names = append(names, n)
		}
	}
	switch shell {
	case "bash":
		b.WriteString("# bash completion for pitwall. Load it with: source <(pitwall completion bash)\n")
		b.WriteString("_pitwall() {\n\tlocal words=\n\tcase $COMP_CWORD in\n")
		fmt.Fprintf(&b, "\t1) words=%q ;;\n\t2)\n\t\tcase ${COMP_WORDS[1]} in\n", strings.Join(top, " "))
		for _, n := range names {
			fmt.Fprintf(&b, "\t\t%s) words=%q ;;\n", n, strings.Join(subs[n], " "))
		}
		b.WriteString("\t\tesac ;;\n\tesac\n\tCOMPREPLY=($(compgen -W \"$words\" -- \"${COMP_WORDS[COMP_CWORD]}\"))\n}\n")
		b.WriteString("complete -o default -F _pitwall pitwall\n")
	case "zsh":
		b.WriteString("#compdef pitwall\n# zsh completion for pitwall. Load it with: source <(pitwall completion zsh), after compinit\n")
		b.WriteString("_pitwall() {\n\tlocal -a cmds\n\tcase $CURRENT in\n")
		fmt.Fprintf(&b, "\t2) cmds=(%s) ;;\n\t3)\n\t\tcase $words[2] in\n", strings.Join(top, " "))
		for _, n := range names {
			fmt.Fprintf(&b, "\t\t%s) cmds=(%s) ;;\n", n, strings.Join(subs[n], " "))
		}
		b.WriteString("\t\tesac ;;\n\tesac\n\tif (( $#cmds )); then compadd -- $cmds; else _files; fi\n}\n")
		b.WriteString("if [ \"$funcstack[1]\" = _pitwall ]; then _pitwall \"$@\"; else compdef _pitwall pitwall; fi\n")
	case "fish":
		b.WriteString("# fish completion for pitwall. Load it with: pitwall completion fish | source\n")
		fmt.Fprintf(&b, "complete -c pitwall -n __fish_use_subcommand -f -a '%s'\n", strings.Join(top, " "))
		for _, n := range names {
			fmt.Fprintf(&b, "complete -c pitwall -n '__fish_seen_subcommand_from %s; and test (count (commandline -opc)) -eq 2' -f -a '%s'\n", n, strings.Join(subs[n], " "))
		}
	default:
		return "", fmt.Errorf("no completion for %q: bash, zsh or fish", shell)
	}
	return b.String(), nil
}
