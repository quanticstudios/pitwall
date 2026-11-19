package chatin

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif" // pictures Thumb and Fit read
	"image/jpeg"
	_ "image/png"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// Thumb checks that b is a picture a chat takes and returns it scaled to
// fit in w by h pixels.
func Thumb(b []byte, w, h int) (*image.NRGBA, error) {
	if len(b) > proto.MaxPasteImage {
		return nil, errors.New("the image is over 20 MiB")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("choose a PNG, JPEG, GIF or WebP picture")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, errors.New("the picture exceeds 40 megapixels")
	}
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width*h > cfg.Height*w {
		h = max(1, w*cfg.Height/cfg.Width)
	} else {
		w = max(1, h*cfg.Width/cfg.Height)
	}
	thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
	bounds := im.Bounds()
	for y := range h {
		for x := range w {
			thumb.Set(x, y, im.At(bounds.Min.X+x*bounds.Dx()/w, bounds.Min.Y+y*bounds.Dy()/h))
		}
	}
	return thumb, nil
}

// The daemon's limits for one image attachment (daemon/attach.go).
const (
	maxSend  = 5 << 20
	fitSide  = 2048 // the long side Fit scales a picture down to
	fitScale = 85   // its JPEG quality
)

var sendTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// Fit is b as a chat attachment takes it: a PNG, JPEG or WebP picture of
// at most 5 MiB is sent as it is; any other picture becomes a JPEG at
// most 2048 pixels on its long side, smaller until it fits.
func Fit(b []byte) ([]byte, string, error) {
	if mime := http.DetectContentType(b); sendTypes[mime] && len(b) <= maxSend {
		return b, mime, nil
	}
	if _, err := Thumb(b, 1, 1); err != nil { // the size and type checks
		return nil, "", err
	}
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, "", err
	}
	for side := fitSide; side >= 256; side /= 2 {
		sz := im.Bounds().Size()
		w, h := sz.X, sz.Y
		if long := max(w, h); long > side {
			w, h = max(1, w*side/long), max(1, h*side/long)
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), im, im.Bounds(), draw.Src, nil)
		var out bytes.Buffer
		if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: fitScale}); err != nil {
			return nil, "", err
		}
		if out.Len() <= maxSend {
			return out.Bytes(), "image/jpeg", nil
		}
	}
	return nil, "", errors.New("the picture does not fit in 5 MiB")
}
