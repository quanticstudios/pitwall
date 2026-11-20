package mdview

import "testing"

// A reply's lines stay lines in a chat; the reader joins them as markdown does.
func TestParseChatKeepsLines(t *testing.T) {
	src := []byte("1\n2\n3")
	text := func(bs []Block) string {
		var s string
		for _, sp := range bs[0].Spans {
			s += sp.Text
		}
		return s
	}
	if got := text(ParseChat(src)); got != "1\n2\n3" {
		t.Errorf("ParseChat: %q", got)
	}
	if got := text(Parse(src)); got != "1 2 3" {
		t.Errorf("Parse: %q", got)
	}
}
