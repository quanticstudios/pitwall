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

// decode checks that b is a picture a chat takes and decodes it.
func decode(b []byte) (image.Image, error) {
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
	return im, err
}

// Thumb checks that b is a picture a chat takes and returns it scaled to
// fit in w by h pixels.
func Thumb(b []byte, w, h int) (*image.NRGBA, error) {
	im, err := decode(b)
	if err != nil {
		return nil, err
	}
	sz := im.Bounds().Size()
	if sz.X*h > sz.Y*w {
		h = max(1, w*sz.Y/sz.X)
	} else {
		w = max(1, h*sz.X/sz.Y)
	}
	thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), im, im.Bounds(), draw.Src, nil)
	return thumb, nil
}

// Cover checks that b is a picture a chat takes and returns its middle
// square, scaled to side by side pixels: a thumbnail cropped to fill.
func Cover(b []byte, side int) (*image.NRGBA, error) {
	im, err := decode(b)
	if err != nil {
		return nil, err
	}
	r := im.Bounds()
	if d := r.Dx() - r.Dy(); d > 0 {
		r.Min.X += d / 2
		r.Max.X = r.Min.X + r.Dy()
	} else {
		r.Min.Y -= d / 2
		r.Max.Y = r.Min.Y + r.Dx()
	}
	side = max(1, side)
	thumb := image.NewNRGBA(image.Rect(0, 0, side, side))
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), im, r, draw.Src, nil)
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
	im, err := decode(b)
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
