// Package tmuxshim implements the tmux argument and format subset used by agent teams.
package tmuxshim

import (
	"fmt"
	"strings"
)

// Adapted from tuios (MIT): internal/tmuxcompat/args.go
// Spec is the flags one command accepts: bools take no value, values take one.
type Spec struct {
	Bools  string
	Values string
}

// Parsed is one command's flags and positional arguments.
type Parsed struct {
	flags map[byte]bool
	vals  map[byte][]string
	// Args are the positional arguments, after the flags or after "--".
	Args []string
}

// Has reports whether bool flag c was given.
func (p Parsed) Has(c byte) bool { return p.flags[c] }

// Value returns the last value given for flag c, and whether it was given.
func (p Parsed) Value(c byte) (string, bool) {
	v := p.vals[c]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// Values returns every value given for flag c, in order.
func (p Parsed) Values(c byte) []string { return p.vals[c] }

// errUnknownFlag is a flag the command's spec does not name.
type errUnknownFlag struct {
	cmd  string
	flag byte
}

func (e errUnknownFlag) Error() string {
	return fmt.Sprintf("%s: unknown flag -%c", e.cmd, e.flag)
}

// ParseFlags reads a command's argv (the command name excluded) the way tmux's
// getopt does: flags first, clustered or not, a value attached or in the next
// word, "--" ending them, and the first word that is not a flag starting the
// positional arguments.
func ParseFlags(cmd string, sp Spec, args []string) (Parsed, error) {
	p := Parsed{flags: map[byte]bool{}, vals: map[byte][]string{}}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		j := 1
		for j < len(a) {
			c := a[j]
			switch {
			case strings.IndexByte(sp.Bools, c) >= 0:
				p.flags[c] = true
				j++
			case strings.IndexByte(sp.Values, c) >= 0:
				val := a[j+1:]
				if val == "" {
					if i+1 >= len(args) {
						return p, fmt.Errorf("%s: -%c needs a value", cmd, c)
					}
					i++
					val = args[i]
				}
				p.vals[c] = append(p.vals[c], val)
				j = len(a)
			default:
				return p, errUnknownFlag{cmd: cmd, flag: c}
			}
		}
		i++
	}
	p.Args = append([]string(nil), args[i:]...)
	return p, nil
}
