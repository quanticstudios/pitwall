package daemon

import (
	"encoding/binary"
	"slices"
	"testing"
)

func TestProcArgs2(t *testing.T) {
	b := binary.NativeEndian.AppendUint32(nil, 3)
	b = append(b, "/usr/local/bin/node\x00\x00\x00\x00node\x00/opt/homebrew/bin/gemini\x00-y\x00HOME=/Users/u\x00"...)
	if got := procArgs2(b, 16); !slices.Equal(got, []string{"node", "/opt/homebrew/bin/gemini", "-y"}) {
		t.Errorf("procArgs2 = %q", got)
	}
	if got := procArgs2(b, 2); len(got) != 2 {
		t.Errorf("max 2: %q", got)
	}
	if got := procArgs2(b[:3], 16); got != nil {
		t.Errorf("short buffer: %q", got)
	}
}
