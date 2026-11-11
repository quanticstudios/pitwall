package mobile

import (
	"testing"
	"time"

	"gioui.org/app"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/qr"
)

// qrFrame draws the code for link into a w by h grey camera frame, off
// center, the way pitwall remote pair shows it.
func qrFrame(t *testing.T, link string, w, h int) []byte {
	t.Helper()
	code, err := qr.Encode(link)
	if err != nil {
		t.Fatal(err)
	}
	const mod, x0, y0 = 5, 230, 90
	y := make([]byte, w*h)
	for i := range y {
		y[i] = 170 // a grey wall
	}
	quiet := 4 * mod
	for py := y0 - quiet; py < y0+len(code)*mod+quiet; py++ {
		for px := x0 - quiet; px < x0+len(code)*mod+quiet; px++ {
			r, c := (py-y0)/mod, (px-x0)/mod
			dark := py >= y0 && px >= x0 && r < len(code) && c < len(code) && code[r][c]
			y[py*w+px] = 235
			if dark {
				y[py*w+px] = 30
			}
		}
	}
	return y
}

// TestDecodeQR reads the code pitwall remote pair draws out of a 640x480
// camera frame and pairs from it.
func TestDecodeQR(t *testing.T) {
	const link = "https://box.example.com/#pair=ABCD2345EFGH6723"
	const w, h = 640, 480
	text, err := decodeQR(qrFrame(t, link, w, h), w, h)
	if err != nil || text != link {
		t.Fatalf("decodeQR = %q, %v; want %q", text, err, link)
	}
	if server, c, err := phone.ParseScan(text); err != nil || server != "https://box.example.com/" || c != "ABCD2345EFGH6723" {
		t.Fatalf("ParseScan = %q %q %v", server, c, err)
	}
	if _, err := decodeQR(make([]byte, w*h), w, h); err == nil {
		t.Fatal("decodeQR found a code in a black frame")
	}
}

// TestCancelledScan cancels a scan while it decodes a code and starts
// another: the first scan's late result and failure leave the second
// alone, and the second still pairs from its own code.
func TestCancelledScan(t *testing.T) {
	type cb struct {
		frame func(frame)
		fail  func(string)
	}
	var opened []cb
	defer func(f func(func(frame), func(string))) { openCamera = f }(openCamera)
	openCamera = func(f func(frame), fail func(string)) { opened = append(opened, cb{f, fail}) }

	a := &App{w: new(app.Window), ui: make(chan func(), 16)}
	a.pair.init()
	next := func() func() {
		t.Helper()
		select {
		case f := <-a.ui:
			return f
		case <-time.After(10 * time.Second):
			t.Fatal("no decode result")
			return nil
		}
	}
	idle := func() {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); a.cam.busy.Load(); time.Sleep(time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("decode never finished")
			}
		}
	}
	const w, h = 640, 480

	a.startScan()
	opened[0].frame(frame{y: qrFrame(t, "https://127.0.0.1:1/#pair=AAAA2222BBBB3333", w, h), w: w, h: h})
	a.stopScan() // Cancel, while the first frame decodes
	a.startScan()
	next()()
	opened[0].fail("The camera stopped.")
	next()()
	if p := &a.pair; !p.scanning || p.busy || p.note != "" || p.link.Text() != "" {
		t.Fatalf("the cancelled scan acted: scanning %v, busy %v, note %q, link %q", p.scanning, p.busy, p.note, p.link.Text())
	}

	idle()
	const link = "https://127.0.0.1:1/#pair=CCCC4444DDDD5555"
	opened[1].frame(frame{y: qrFrame(t, link, w, h), w: w, h: h})
	next()()
	if p := &a.pair; p.scanning || !p.busy || p.link.Text() != link {
		t.Fatalf("the second scan did not pair: scanning %v, busy %v, link %q", p.scanning, p.busy, p.link.Text())
	}
}
