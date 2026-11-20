package panel

import (
	"strings"

	"github.com/quanticstudios/pitwall/internal/ui/syntax"
)

// runLangs are the fences whose blocks get Run.
var runLangs = map[string]bool{"sh": true, "bash": true, "zsh": true, "console": true, "shell": true, "shellsession": true, "terminal": true, "fish": true, "ksh": true}

// runnable is the command a block's Run types: a shell block's lines
// without blank lines and comments, or a session's commands without
// their "$ " prompts and output; "" when the block is no shell block,
// as a block in another language or without one and without a prompt.
func runnable(lang, code string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, " {,"); i >= 0 {
		l = l[:i]
	}
	prompted := syntax.Prompted(code)
	if !runLangs[l] && !(l == "" && prompted) {
		return ""
	}
	var out []string
	cont := false // the line before ended in a backslash
	for line := range strings.SplitSeq(code, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case cont:
		case prompted && (t == "$" || strings.HasPrefix(t, "$ ")):
			t = strings.TrimSpace(strings.TrimPrefix(t, "$"))
		case prompted:
			continue // output
		}
		cont = strings.HasSuffix(t, "\\")
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

// OneLine is cmd, a Run's lines, as one line a shell without bracketed
// paste takes without running any of it: lines joined by "; ", or by a
// space after a backslash, a pipe, && or ||, do, then, else or {.
func OneLine(cmd string) string {
	var b strings.Builder
	for i, line := range strings.Split(cmd, "\n") {
		if i > 0 {
			prev := b.String()
			switch {
			case strings.HasSuffix(prev, "\\"):
				b.Reset()
				b.WriteString(strings.TrimRight(strings.TrimSuffix(prev, "\\"), " "))
				b.WriteString(" ")
			case endsOpen(prev):
				b.WriteString(" ")
			default:
				b.WriteString("; ")
			}
		}
		b.WriteString(line)
	}
	return b.String()
}

// endsOpen reports whether a shell line ends where the next line goes
// on with the same command.
func endsOpen(s string) bool {
	for _, end := range []string{"|", "&&", "||", ";", "{", "(", " do", " then", " else"} {
		if strings.HasSuffix(s, end) || s == strings.TrimSpace(end) {
			return true
		}
	}
	return false
}
