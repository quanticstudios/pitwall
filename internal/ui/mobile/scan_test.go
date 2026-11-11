package mobile

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/qr"
)

// TestDecodeQR reads the code pitwall remote pair draws out of a 640x480
// camera frame, grey with the code off-center, and pairs from it.
func TestDecodeQR(t *testing.T) {
	const link = "https://box.example.com/#pair=ABCD2345EFGH6723"
	code, err := qr.Encode(link)
	if err != nil {
		t.Fatal(err)
	}
	const w, h, mod, x0, y0 = 640, 480, 5, 230, 90
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
	text, err := decodeQR(y, w, h)
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
