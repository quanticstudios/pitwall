package daemon

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// userKeys separates keys from reports within one batch of terminal input.
// shortcut: GUI batches contain whole events and pastes; track parser state per pane if a streaming client needs it.
func userKeys(data []byte) (pressed, edited, enter bool) {
	paste := false
	var state byte
	for s := string(data); len(s) > 0; {
		seq, width, n, next := ansi.DecodeSequence(s, state, nil)
		state = next
		if n == 0 {
			break
		}
		if seq == "\x1b[M" { // X10 mouse has three raw payload bytes.
			n = min(len(s), n+3)
		}
		if seq == "\x1bO" && len(s) > n {
			n++ // SS3 keys include a final byte the ANSI decoder leaves as text.
			seq = s[:n]
		}
		s = s[n:]
		switch seq {
		case "\x1b[200~":
			paste = true
			continue
		case "\x1b[201~":
			paste = false
			continue
		}
		key, edit, submit := false, false, false
		r, _ := utf8.DecodeRuneInString(seq)
		switch {
		case paste, width > 0, unicode.IsPrint(r):
			key, edit = true, true
		case strings.HasPrefix(seq, "\x1b["):
			key, edit, submit = csiKey(seq)
		case strings.HasPrefix(seq, "\x1bO") && len(seq) == 3:
			key = true
			edit = strings.ContainsRune("ABCM", rune(seq[2]))
			submit = seq[2] == 'M'
		case len(seq) == 1:
			key, edit, submit = true, editsControl(int(seq[0])), seq[0] == '\r'
		case len(seq) == 2 && seq[0] == '\x1b':
			key = true
			// Alt+B/F move by words; other Alt keys may edit the prompt.
			c := unicode.ToLower(rune(seq[1]))
			edit = c != 'b' && c != 'f'
		}
		pressed = pressed || key
		if edit {
			edited, enter = true, submit
		}
	}
	return
}

func csiKey(seq string) (pressed, edited, enter bool) {
	if len(seq) < 3 {
		return
	}
	final := seq[len(seq)-1]
	fields := strings.Split(seq[2:len(seq)-1], ";")
	code, err := strconv.Atoi(strings.Split(fields[0], ":")[0])
	if fields[0] == "" {
		code, err = 1, nil
	}
	if err != nil { // Private CSI parameters identify reports, including SGR mouse.
		return
	}
	mods := 1
	if len(fields) > 1 {
		parts := strings.Split(fields[1], ":")
		mods, err = strconv.Atoi(parts[0])
		if err != nil || len(parts) > 1 && parts[1] == "3" {
			return // Kitty releases cannot change the input box.
		}
	}
	switch final {
	case 'u':
		pressed = true
		if code == 13 || code == 57414 {
			return true, true, mods == 1
		}
		if mods > 0 && (mods-1)&4 != 0 {
			if c := int(unicode.ToLower(rune(code))); c >= 'a' && c <= 'z' || c >= '[' && c <= '_' || c == '@' {
				code = c & 0x1f
			}
			return true, editsControl(code), false
		}
		if c := unicode.ToLower(rune(code)); mods > 0 && (mods-1)&2 != 0 && (c == 'b' || c == 'f') {
			return true, false, false
		}
		edited = editsControl(code) || unicode.IsPrint(rune(code))
	case 'A', 'B', 'C', 'D', 'H', 'F', 'P', 'Q', 'S', 'Z':
		pressed = true
		edited = strings.ContainsRune("ABCZ", rune(final))
	case '~':
		pressed = true
		edited = code == 3 // Delete, rather than Insert, paging or function keys.
	}
	return
}

func editsControl(code int) bool {
	switch code {
	case 0, 4, 7, 8, 9, 10, 11, 13, 14, 16, 18, 19, 20, 21, 22, 23, 24, 25, 31, 127:
		return true // Completion, deletion, newline, history, search, paste and undo.
	}
	return false
}
