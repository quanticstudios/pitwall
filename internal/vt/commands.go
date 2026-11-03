package vt

import (
	"slices"
	"strings"
	"time"
)

// CommandBlock is a shell command delimited by OSC 133, with rendered text.
// Output keeps line breaks and omits terminal escape sequences.
type CommandBlock struct {
	Prompt                Pos
	Command, Output       string
	Started               time.Time
	ExitCode              int
	Finished, StatusKnown bool
}

// Copy returns the command, output, or both, without the shell prompt.
func (b CommandBlock) Copy(part string) string {
	switch part {
	case "command":
		return b.Command
	case "output":
		return b.Output
	case "both":
		if b.Output == "" {
			return b.Command
		}
		return b.Command + "\n" + b.Output
	}
	return ""
}

type shellMark struct {
	At    Pos
	Kind  string
	Time  time.Time
	Code  int
	Known bool
}

func (t *emulator) pruneShell() {
	oldest := t.st.hist.pushed - uint64(t.st.hist.len())
	t.st.shell = slices.DeleteFunc(t.st.shell, func(m shellMark) bool { return m.At.Line < oldest })
	// A shell can redraw the same prompt without adding history.
	limit := 4 * (t.st.hist.limit() + t.e.Height())
	if len(t.st.shell) > limit {
		t.st.shell = slices.Clone(t.st.shell[len(t.st.shell)-limit:])
	}
}

// Commands returns retained commands, oldest first, and whether a prompt mark
// exists. It never infers commands from text that resembles a shell prompt.
func (t *emulator) Commands() ([]CommandBlock, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.e.IsAltScreen() {
		return nil, false
	}
	g := snapshot(t.e, t.st)
	h := &t.st.hist
	screen := GridLines(&g, h.pushed)
	oldest := h.pushed - uint64(h.len())
	var buf []Cell
	lines := func(n uint64) ([]Cell, bool, bool) {
		if n >= h.pushed || n < oldest {
			return screen(n)
		}
		l := h.at(int(n - oldest))
		// Packing can omit the empty right cell of a final wide character.
		buf = append(buf[:0], make([]Cell, l.width()+1)...)
		l.fill(buf)
		return buf[:l.width()], l.wrapped(), true
	}
	text := func(a, b Pos) string {
		if !a.Before(b) {
			return ""
		}
		b.Col-- // Selection includes its last cell; shell marks are exclusive.
		return (Selection{A: a, B: b}).Text(lines)
	}
	var out []CommandBlock
	var block *CommandBlock
	var input, output *Pos
	integrated := false
	finish := func(end Pos) {
		if block != nil && output != nil {
			block.Output = text(*output, end)
			out = append(out, *block)
		}
		block, input, output = nil, nil, nil
	}
	for _, m := range t.st.shell {
		switch m.Kind {
		case "A":
			integrated = true
			finish(m.At)
			block = &CommandBlock{Prompt: m.At}
		case "B":
			if block != nil && output == nil {
				p := m.At
				input = &p
			}
		case "C":
			if block != nil && input != nil && output == nil {
				block.Command = strings.TrimSpace(text(*input, m.At))
				block.Started = m.Time
				p := m.At
				output = &p
			}
		case "D":
			if block != nil && output != nil {
				block.Finished, block.ExitCode, block.StatusKnown = true, m.Code, m.Known
				finish(m.At)
			}
		}
	}
	_, _, cur, _ := t.e.MainLines()
	finish(Pos{Line: h.pushed + uint64(cur.Y), Col: cur.X})
	return out, integrated
}

// HasPromptMarks reports whether retained text contains an OSC 133 prompt.
func (t *emulator) HasPromptMarks() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.e.IsAltScreen() {
		return false
	}
	return slices.ContainsFunc(t.st.shell, func(m shellMark) bool { return m.Kind == "A" })
}
