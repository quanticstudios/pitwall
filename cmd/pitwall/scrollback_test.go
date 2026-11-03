//go:build unix

package main

import (
	"strings"
	"testing"
)

func TestCLIScrollback(t *testing.T) {
	_, run := talkDaemon(t)
	run(0, "#1\n", "new", "-n", "output", "--", "sh", "-c", `printf '\033[31mfirst\033[0m\n'; printf '%s\nlast\n' '`+strings.Repeat("x", 110)+`'`)
	run(0, "", "wait", "output", "--until", "exit", "--timeout", "5s")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"output"}, "first\n" + strings.Repeat("x", 110) + "\nlast\n"},
		{[]string{"output", "--lines", "1"}, "last\n"},
		{[]string{"--lines", "2", "output"}, strings.Repeat("x", 110) + "\nlast\n"},
	} {
		code, out, stderr := cliOutput(append([]string{"scrollback"}, tc.args...)...)
		if code != 0 || out != tc.want {
			t.Fatalf("scrollback %v: %d %q %q; want %q", tc.args, code, out, stderr, tc.want)
		}
	}
	run(1, "", "scrollback", "output", "--lines", "-1")
	run(1, "", "scrollback", "missing")
}
