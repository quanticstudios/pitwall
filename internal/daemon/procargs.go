package daemon

import (
	"encoding/binary"
	"strings"
)

// procArgs2 is the argv in a macOS kern.procargs2 buffer: a 32-bit argc,
// the executable's path, NUL padding, then argc NUL-terminated arguments
// (the environment follows). At most max arguments are returned.
func procArgs2(b []byte, max int) []string {
	if len(b) < 4 {
		return nil
	}
	argc := int(binary.NativeEndian.Uint32(b))
	rest := string(b[4:])
	i := strings.IndexByte(rest, 0)
	if i < 0 {
		return nil
	}
	rest = strings.TrimLeft(rest[i:], "\x00")
	var out []string
	for len(out) < min(argc, max) {
		a, after, ok := strings.Cut(rest, "\x00")
		if !ok {
			break
		}
		out, rest = append(out, a), after
	}
	return out
}
