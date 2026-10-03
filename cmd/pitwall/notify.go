package main

import (
	"errors"
	"io"
	"os"
	"strings"
)

// runNotify asks for attention from the calling pane: it writes text as an
// OSC 9 notification to the terminal, which pitwall turns into a ring.
// Stdout may be a pipe in a script, so it prefers the controlling terminal.
func runNotify(args []string) error {
	text := strings.Join(args, " ")
	if text == "" {
		return errors.New(`usage: pitwall notify <text>`)
	}
	var w io.Writer = os.Stdout
	if tty, err := os.OpenFile(ttyPath, os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		w = tty
	}
	_, err := io.WriteString(w, oscNotify(text))
	return err
}

// oscNotify is text as an OSC 9 sequence, with control bytes that would end
// or break the sequence replaced by spaces.
func oscNotify(text string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, text)
	return "\x1b]9;" + clean + "\a"
}
