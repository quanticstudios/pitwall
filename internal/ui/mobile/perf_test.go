package mobile

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/phone"
)

const perfMarkdown = "The **cache** keeps `entry.Text` and its layout. Read the [guide](https://example.com) before changing it.\n\n"

func TestChatScrollAllocationsIgnoreOffscreenEntries(t *testing.T) {
	th := Theme()
	allocs := func(n int) float64 {
		c := newChat("p1")
		for i := range n {
			c.data.Entries = append(c.data.Entries, flow.Entry{Seq: int64(i + 1), Kind: flow.EntryAssistant, Text: perfMarkdown})
		}
		var ops op.Ops
		gtx := C{Ops: &ops, Now: time.Unix(1_800_000_000, 0), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(412, 700))}
		return testing.AllocsPerRun(10, func() {
			ops.Reset()
			c.list.Position = layout.Position{First: 20, BeforeEnd: true}
			c.log(gtx, th, chatEnv{})
		})
	}
	small, large := allocs(200), allocs(2000)
	if large > small+4 {
		t.Fatalf("offscreen entries add allocations per frame: 200 = %.0f, 2000 = %.0f", small, large)
	}
}

func TestReplyCacheTracksTextAndWidth(t *testing.T) {
	th := Theme()
	c := newChat("p1")
	e := flow.Entry{Seq: 1, Kind: flow.EntryAssistant, Text: perfMarkdown}
	c.add(phone.Entries{Entries: []flow.Entry{e}})
	var ops op.Ops
	gtx := C{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(412, 10000)}}
	wide := c.entry(gtx, th, c.data.Entries[0])
	r := c.row(e.Seq)
	spans := &r.parts[0].spans[0]
	ops.Reset()
	c.entry(gtx, th, c.data.Entries[0])
	if &r.parts[0].spans[0] != spans {
		t.Fatal("unchanged entry reparsed markdown")
	}
	ops.Reset()
	gtx.Constraints.Max.X = 160
	narrow := c.entry(gtx, th, c.data.Entries[0])
	if narrow.Size.Y <= wide.Size.Y || &r.parts[0].spans[0] != spans {
		t.Fatal("width change must reflow cached markdown without parsing")
	}
	ops.Reset()
	e.Text = strings.Repeat(perfMarkdown, 4)
	c.add(phone.Entries{Entries: []flow.Entry{e}})
	changed := c.entry(gtx, th, c.data.Entries[0])
	if changed.Size.Y <= narrow.Size.Y || &r.parts[0].spans[0] == spans {
		t.Fatal("changed entry reused stale markdown or layout")
	}
}

func BenchmarkReply(b *testing.B) {
	th := Theme()
	c := newChat("p1")
	r := c.row(1)
	src := strings.Repeat(perfMarkdown, 8)
	var ops op.Ops
	gtx := C{Ops: &ops, Now: time.Unix(1_800_000_000, 0), Metric: unit.Metric{PxPerDp: 2.625, PxPerSp: 2.625}, Constraints: layout.Constraints{Max: image.Pt(996, 1000000)}}
	c.reply(gtx, th, r, src)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ops.Reset()
		c.reply(gtx, th, r, src)
	}
}

func BenchmarkChatLog(b *testing.B) {
	for _, n := range []int{200, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			th := Theme()
			c := newChat("p1")
			for i := range n {
				c.data.Entries = append(c.data.Entries, flow.Entry{Seq: int64(i + 1), Kind: flow.EntryAssistant, Text: perfMarkdown})
			}
			var ops op.Ops
			gtx := C{Ops: &ops, Now: time.Unix(1_800_000_000, 0), Metric: unit.Metric{PxPerDp: 2.625, PxPerSp: 2.625}, Constraints: layout.Exact(image.Pt(1080, 1800))}
			c.log(gtx, th, chatEnv{})
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				ops.Reset()
				c.list.Position = layout.Position{First: n / 2, Offset: i % 200, BeforeEnd: true}
				c.log(gtx, th, chatEnv{})
			}
		})
	}
}
