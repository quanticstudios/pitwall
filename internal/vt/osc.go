package vt

import (
	"bytes"
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// Notification is a desktop notification a program asked for with OSC 9
// (iTerm2), OSC 777 (urxvt, foot, Ghostty) or OSC 99 (kitty). Either field
// may be empty, not both.
type Notification struct {
	Title, Body string
}

// oscMax bounds one buffered OSC payload; anything longer passes through
// untouched, as x/vt would have seen it.
const oscMax = 64 << 10

// oscFilter pulls OSC sequences out of the stream before x/vt parses them.
// why: x/ansi treats byte 0x9C as the C1 string terminator even inside a
// UTF-8 character, so an OSC whose payload holds one (Claude Code's "✳"
// title prefix is E2 9C B3) is cut short and its tail is printed on screen.
// Titles (OSC 0, 1, 2) and notifications (OSC 9, 99, 777) are handled here;
// other OSCs go through unless their payload holds 0x9C, in which case they
// are dropped.
type oscFilter struct {
	state int // 0 text, 1 after ESC, 2 in OSC, 3 in OSC after ESC
	buf   []byte
}

// feed returns the bytes x/vt should parse and calls title for each title
// sequence it removes, notify for each notification. State carries over between calls, so a sequence split
// across PTY reads is still caught.
func (f *oscFilter) feed(p []byte, title func(string), notify func(Notification)) []byte {
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
				out = f.end(out, title, notify, "\x07")
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
				out = f.end(out, title, notify, "\x1b\\")
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

func (f *oscFilter) end(out []byte, title func(string), notify func(Notification), term string) []byte {
	f.state = 0
	cmd, payload, _ := bytes.Cut(f.buf, []byte(";"))
	switch string(cmd) {
	case "0", "1", "2":
		title(validUTF8(string(payload)))
		return out
	case "9", "99", "777":
		// Nothing on screen depends on these, so they never reach x/vt.
		if n, ok := parseNotification(string(cmd), string(payload)); ok && notify != nil {
			notify(n)
		}
		return out
	}
	if bytes.IndexByte(f.buf, 0x9c) >= 0 {
		return out
	}
	out = append(append(out, 0x1b, ']'), f.buf...)
	return append(out, term...)
}

func validUTF8(s string) string { return strings.ToValidUTF8(s, string(utf8.RuneError)) }

// parseNotification reads the payload of OSC 9, 99 or 777. It skips
// ConEmu's numeric OSC 9 subcommands (9;4;... is progress), OSC 777 other
// than notify, and kitty chunks that are not the last (d=0) or carry
// something other than a title or body.
func parseNotification(cmd, payload string) (Notification, bool) {
	var n Notification
	switch cmd {
	case "9":
		sub, _, _ := strings.Cut(payload, ";")
		if sub != "" && strings.Trim(sub, "0123456789") == "" {
			return n, false
		}
		n.Body = payload
	case "777":
		parts := strings.SplitN(payload, ";", 3)
		if parts[0] != "notify" || len(parts) < 2 {
			return n, false
		}
		n.Title = parts[1]
		if len(parts) == 3 {
			n.Body = parts[2]
		}
	case "99":
		meta, text, ok := strings.Cut(payload, ";")
		if !ok {
			return n, false
		}
		kind, b64 := "title", false
		for kv := range strings.SplitSeq(meta, ":") {
			k, v, _ := strings.Cut(kv, "=")
			switch {
			case k == "d" && v != "1":
				return n, false
			case k == "p":
				kind = v
			case k == "e":
				b64 = v == "1"
			}
		}
		if b64 {
			dec, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return n, false
			}
			text = string(dec)
		}
		switch kind {
		case "title":
			n.Title = text
		case "body":
			n.Body = text
		default:
			return n, false
		}
	}
	n.Title, n.Body = validUTF8(strings.TrimSpace(n.Title)), validUTF8(strings.TrimSpace(n.Body))
	return n, n.Title != "" || n.Body != ""
}
