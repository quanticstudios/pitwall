package mobile

import (
	"image"
	"math"
	"sync/atomic"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// frame is one camera picture's brightness, w by h bytes, which the
// viewfinder turns rot degrees clockwise to stand upright.
type frame struct {
	y         []byte
	w, h, rot int
}

// camFeed is what the camera's thread hands the window: the newest frame
// and whether a decode runs. Pairing's state resets wholesale, so this
// lives on App. gen counts ended scans, on the window's goroutine only,
// so a scan's late decode or failure cannot act on the scan after it.
type camFeed struct {
	latest atomic.Pointer[frame]
	busy   atomic.Bool
	gen    int
}

// openCamera is startCamera, which tests replace.
var openCamera = startCamera

// decodeQR reads a QR code from a w by h picture's brightness bytes.
func decodeQR(y []byte, w, h int) (string, error) {
	src, err := gozxing.NewPlanarYUVLuminanceSource(y, w, h, 0, 0, w, h, false)
	if err != nil {
		return "", err
	}
	bmp, err := gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(src))
	if err != nil {
		return "", err
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		return "", err
	}
	return res.GetText(), nil
}

// startScan opens the camera and pairs with the first pairing link it
// reads. The camera asks for its permission first.
func (a *App) startScan() {
	p := &a.pair
	p.scanning, p.note = true, ""
	gen := a.cam.gen
	openCamera(func(f frame) {
		a.cam.latest.Store(&f)
		a.w.Invalidate()
		if !a.cam.busy.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer a.cam.busy.Store(false)
			text, err := decodeQR(f.y, f.w, f.h)
			if err != nil {
				return // no code in this frame
			}
			server, code, err := phone.ParseScan(text)
			a.post(func() {
				if a.cam.gen != gen {
					return
				}
				if err != nil {
					p.note = sentence(err.Error())
					return
				}
				a.stopScan()
				p.link.SetText(text)
				a.startPair(server, code, "")
			})
		}()
	}, func(why string) {
		a.post(func() {
			if a.cam.gen == gen {
				a.stopScan()
				p.note = why
			}
		})
	})
}

func (a *App) stopScan() {
	stopCamera()
	a.cam.gen++
	a.pair.scanning, a.pair.shown = false, nil
	a.cam.latest.Store(nil)
}

func (a *App) scanStep() []layout.Widget {
	p, th := &a.pair, a.th
	w := a.pairHead("Scan the QR code", "Point the camera at the code ", "pitwall remote pair", " or Settings, Phone shows.")
	w = append(w, a.viewfinder)
	w = append(w, a.pairNote()...)
	return append(w, gap(14), func(gtx C) D {
		return layout.Center.Layout(gtx, func(gtx C) D { return textButton(gtx, th, &p.cancelScan, "Cancel") })
	})
}

// viewfinder is a square of the newest camera frame, turned upright and
// cropped to fill it.
func (a *App) viewfinder(gtx C) D {
	p, th := &a.pair, a.th
	s := min(gtx.Constraints.Max.X, gtx.Dp(320))
	size := image.Pt(s, s)
	return layout.Center.Layout(gtx, func(gtx C) D {
		defer clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl)).Push(gtx.Ops).Pop()
		paint.Fill(gtx.Ops, th.Surface)
		if f := a.cam.latest.Load(); f != nil {
			if f != p.shown {
				p.shown = f
				p.img = paint.NewImageOp(&image.Gray{Pix: f.y, Stride: f.w, Rect: image.Rect(0, 0, f.w, f.h)})
			}
			dw, dh := float32(f.w), float32(f.h)
			if f.rot%180 != 0 {
				dw, dh = dh, dw
			}
			k := float32(s) / min(dw, dh)
			tr := f32.Affine2D{}.
				Offset(f32.Pt(-float32(f.w)/2, -float32(f.h)/2)).
				Rotate(f32.Point{}, float32(f.rot)*math.Pi/180).
				Scale(f32.Point{}, f32.Pt(k, k)).
				Offset(f32.Pt(float32(s)/2, float32(s)/2))
			st := op.Affine(tr).Push(gtx.Ops)
			p.img.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			st.Pop()
		}
		return D{Size: size}
	})
}

// scanEvents handles Cancel and Back while scanning, and reports whether
// the scan ended.
func (a *App) scanEvents(gtx C) bool {
	p := &a.pair
	ended := p.cancelScan.Clicked(gtx)
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			ended = true
		}
	}
	if ended {
		a.stopScan()
		p.note = ""
	}
	return ended
}
