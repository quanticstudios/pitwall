package vt

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// oscMax bounds one buffered OSC payload; anything longer passes through
// untouched, as x/vt would have seen it.
const oscMax = 64 << 10

// oscFilter pulls OSC sequences out of the stream before x/vt parses them.
// why: x/ansi treats byte 0x9C as the C1 string terminator even inside a
// UTF-8 character, so an OSC whose payload holds one (Claude Code's "✳"
// title prefix is E2 9C B3) is cut short and its tail is printed on screen.
// Titles (OSC 0, 1, 2) are handled here; other OSCs go through unless their
// payload holds 0x9C, in which case they are dropped.
type oscFilter struct {
	state int // 0 text, 1 after ESC, 2 in OSC, 3 in OSC after ESC
	buf   []byte
}

// feed returns the bytes x/vt should parse and calls title for each title
// sequence it removes. State carries over between calls, so a sequence split
// across PTY reads is still caught.
func (f *oscFilter) feed(p []byte, title func(string)) []byte {
	out := make([]byte, 0, len(p))
	for _, c := range p {
		switch f.state {
		case 0:
			if c == 0x1b {
				f.state = 1
				continue
			}
			out = append(out, c)
		case 1:
			if c == ']' {
				f.state, f.buf = 2, f.buf[:0]
				continue
			}
			f.state = 0
			out = append(out, 0x1b)
			if c == 0x1b {
				f.state = 1
				continue
			}
			out = append(out, c)
		case 2:
			switch {
			case c == 0x07:
				out = f.end(out, title, "\x07")
			case c == 0x1b:
				f.state = 3
			case len(f.buf) >= oscMax:
				out = append(append(append(out, 0x1b, ']'), f.buf...), c)
				f.state = 0
			default:
				f.buf = append(f.buf, c)
			}
		case 3:
			if c == '\\' {
				out = f.end(out, title, "\x1b\\")
				continue
			}
			// ESC that is not ST: x/vt aborts the OSC, so it is dropped here
			// too and the ESC starts the next sequence.
			f.state = 1
			if c == ']' {
				f.state, f.buf = 2, f.buf[:0]
				continue
			}
			f.state = 0
			out = append(out, 0x1b, c)
		}
	}
	return out
}

func (f *oscFilter) end(out []byte, title func(string), term string) []byte {
	f.state = 0
	cmd, payload, _ := bytes.Cut(f.buf, []byte(";"))
	switch string(cmd) {
	case "0", "1", "2":
		title(strings.ToValidUTF8(string(payload), string(utf8.RuneError)))
		return out
	}
	if bytes.IndexByte(f.buf, 0x9c) >= 0 {
		return out
	}
	out = append(append(out, 0x1b, ']'), f.buf...)
	return append(out, term...)
}
