package panel

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pngData is a w by h PNG of c shading to black, base64 as a transcript
// holds it.
func pngData(w, h int, c color.NRGBA) string {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			k := 255 - 120*x/w
			img.SetNRGBA(x, y, color.NRGBA{uint8(int(c.R) * k / 255), uint8(int(c.G) * k / 255), uint8(int(c.B) * k / 255), 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

var (
	thumbRed  = color.NRGBA{230, 40, 40, 255}
	thumbBlue = color.NRGBA{40, 90, 230, 255}
)

// thumbsSnap is a chat of one prompt with atts and a reply.
func thumbsSnap(text string, cut bool, atts ...session.Attachment) *session.Snapshot {
	at := time.Date(2026, 11, 21, 14, 1, 0, 0, time.UTC).Format(time.RFC3339Nano)
	t1 := "t1"
	return &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: text, TurnID: &t1, CreatedAt: at, UpdatedAt: at, Cut: cut, Attachments: atts},
		{ID: "a1", Kind: session.KindAssistantText, Text: "Looked at it.", TurnID: &t1, CreatedAt: at, UpdatedAt: at},
	}}
}

func imageAtt(n string, data string) session.Attachment {
	return session.Attachment{ID: "image-" + n, Kind: "image", MimeType: "image/png", Name: "image-" + n + ".png", Data: data}
}

// thumbsCases are the prompts the shots and the layout test draw.
func thumbsCases() []struct {
	name string
	snap *session.Snapshot
} {
	return []struct {
		name string
		snap *session.Snapshot
	}{
		{"one", thumbsSnap("[Image #5] claude and codex are fine, agy is obviously not", false, imageAtt("1", pngData(320, 200, thumbRed)))},
		{"two", thumbsSnap("[Image #1] [Image #2] which of these two reads better?", false, imageAtt("1", pngData(320, 200, thumbRed)), imageAtt("2", pngData(160, 240, thumbBlue)))},
		{"loading", thumbsSnap("[Image #1] the data is on its way", true, imageAtt("1", ""))},
		{"gone", thumbsSnap("[Image #1] the transcript lost this one", false, imageAtt("1", ""))},
	}
}

// thumbsFrame draws s in chatLayout until its thumbnails decoded.
func thumbsFrame(t *testing.T, s *session.Snapshot, chatLayout string) *image.RGBA {
	t.Helper()
	const w, h = 900, 360
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	th := theme.Dark()
	tr := Transcript{Dir: "/repo", ChatLayout: chatLayout, ToolCalls: ToolsAll, Width: 760}
	var ops op.Ops
	now := time.Date(2026, 11, 21, 14, 4, 0, 0, time.UTC)
	ready := func() bool {
		tr.thumbs.mu.Lock()
		defer tr.thumbs.mu.Unlock()
		for _, tb := range tr.thumbs.m {
			if !tb.ready {
				return false
			}
		}
		return true
	}
	for i := 0; i < 4 || !ready() && i < 400; i++ {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h)), Now: now}
		tr.Layout(gtx, th, s, false)
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		if i >= 3 && !ready() {
			time.Sleep(5 * time.Millisecond)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img
}

// box is the bounds of the pixels in img that match.
func box(img *image.RGBA, match func(color.RGBA) bool) image.Rectangle {
	var r image.Rectangle
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			if match(img.RGBAAt(x, y)) {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

func reddish(c color.RGBA) bool { return c.R > 120 && c.G < 60 && c.B < 60 }

// TestPromptThumbsAbove: in Ledger and Column a prompt's picture draws
// above its text, the text starting at the picture's left edge, the
// theme's small space between; a prompt whose data has not come draws an
// empty face, and one whose picture is gone, no picture.
func TestPromptThumbsAbove(t *testing.T) {
	cs := thumbsCases()
	for _, l := range []string{LayoutLedger, LayoutColumn} {
		img := thumbsFrame(t, cs[0].snap, l)
		pic := box(img, reddish)
		if pic.Empty() || pic.Dy() < 90 || pic.Dy() > 98 {
			t.Fatalf("%s: picture at %v, want one 96 px tall", l, pic)
		}
		under := img.SubImage(image.Rect(pic.Min.X-2, pic.Max.Y, pic.Min.X+400, pic.Max.Y+60)).(*image.RGBA)
		bg := img.RGBAAt(pic.Min.X-2, pic.Max.Y+2) // the bubble's fill in Column, the page's in Ledger
		text := box(under, func(c color.RGBA) bool {
			d := func(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }
			return d(c.R, bg.R)+d(c.G, bg.G)+d(c.B, bg.B) > 24
		})
		if text.Empty() {
			t.Fatalf("%s: no text under the picture at %v", l, pic)
		}
		if gap, s := text.Min.Y-pic.Max.Y, int(theme.SpaceS); gap < s-2 || gap > s+16 {
			t.Errorf("%s: text %d px under the picture, want about SpaceS", l, gap)
		}
		if dx := text.Min.X - pic.Min.X; dx < -3 || dx > 3 { // the fill starts inside the thumbnail's 1 px border
			t.Errorf("%s: text starts %d px from the picture's left edge", l, dx)
		}
		above := img.SubImage(image.Rect(pic.Min.X, 0, pic.Min.X+400, pic.Min.Y)).(*image.RGBA)
		if b := box(above, func(c color.RGBA) bool { return c.R > 150 && c.G > 150 && c.B > 150 }); !b.Empty() {
			t.Errorf("%s: text above the picture at %v", l, b)
		}
		for _, c := range cs[2:] {
			if p := box(thumbsFrame(t, c.snap, l), reddish); !p.Empty() {
				t.Errorf("%s %s: a picture drew at %v", l, c.name, p)
			}
		}
	}
}

// TestPromptThumbShots renders each case in Ledger and Column to
// PITWALL_THUMB_SHOTS for the PR.
func TestPromptThumbShots(t *testing.T) {
	dir := os.Getenv("PITWALL_THUMB_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_THUMB_SHOTS to render the PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range thumbsCases() {
		for _, l := range []string{LayoutLedger, LayoutColumn} {
			f, err := os.Create(filepath.Join(dir, "thumbs-"+c.name+"-"+l+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, thumbsFrame(t, c.snap, l))
			f.Close()
		}
	}
}
