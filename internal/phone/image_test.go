package phone

import (
	"github.com/quanticstudios/pitwall/internal/flow"
	"testing"
)

func TestImagePending(t *testing.T) {
	for _, text := range []string{"Describe the colors.", ""} {
		c := Chat{}
		p := c.Send(text)
		p.Image = true
		c.Add([]flow.Entry{{Seq: 1, Kind: flow.EntryUser, Text: text + "[Image #1]"}})
		if c.Has(p) {
			t.Fatal("image message stayed pending")
		}
	}
	c := Chat{}
	p := c.Send("hello")
	c.Add([]flow.Entry{{Seq: 1, Kind: flow.EntryUser, Text: "hello[Image #1]"}})
	if !c.Has(p) {
		t.Fatal("settled a text message against a different prompt")
	}
}
