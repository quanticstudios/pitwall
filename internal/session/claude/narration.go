package claude

import (
	"encoding/base64"
	"encoding/binary"
)

// narration reports whether a thinking block's signature marks it as the
// server's summary of Claude's words between tool calls, not Claude's
// reasoning. Claude Code 2.1.329 reads the same mark: the signature is
// base64 protobuf, and field 2, then its field 1, then its field 8 holds
// the block's kind, "narration". Anything it cannot read is reasoning.
func narration(sig string) bool {
	b, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	for _, f := range []uint64{2, 1, 8} {
		if b = pbField(b, f); b == nil {
			return false
		}
	}
	return string(b) == "narration"
}

// pbField is the last length-delimited field n of protobuf message b, nil
// when b has none or does not parse.
func pbField(b []byte, n uint64) []byte {
	var out []byte
	for len(b) > 0 {
		key, k := binary.Uvarint(b)
		if k <= 0 {
			return nil
		}
		b = b[k:]
		switch key & 7 {
		case 0:
			_, k = binary.Uvarint(b)
			if k <= 0 {
				return nil
			}
			b = b[k:]
		case 1, 5:
			w := 8
			if key&7 == 5 {
				w = 4
			}
			if len(b) < w {
				return nil
			}
			b = b[w:]
		case 2:
			l, k := binary.Uvarint(b)
			if k <= 0 || l > uint64(len(b)-k) {
				return nil
			}
			if key>>3 == n {
				out = b[k : k+int(l)]
			}
			b = b[k+int(l):]
		default:
			return nil
		}
	}
	return out
}
