package mdview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // the first frame
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Caps on what an image may cost.
const (
	MaxImageBytes  = 20 << 20 // a local file
	MaxRemoteBytes = 5 << 20  // a download
	MaxPixels      = 40e6     // decoded, before scaling
	MaxSide        = 2048     // longest side kept after decoding
	RemoteTimeout  = 10 * time.Second
)

// Where is an image's source resolved: a local path, or an http(s) URL.
type Where struct {
	Path, URL string
}

// ResolveImage is where src points from a file in dir. A data: URL or
// another scheme gives an error, which the reader shows with the alt
// text.
func ResolveImage(src, dir string) (Where, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return Where{}, errors.New("no source")
	}
	if u, err := url.Parse(src); err == nil && len(u.Scheme) > 1 {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			return Where{URL: src}, nil
		case "file":
			return Where{Path: filepath.FromSlash(u.Path)}, nil
		}
		return Where{}, fmt.Errorf("%s: images are not read", u.Scheme)
	}
	p, _, _ := strings.Cut(src, "#")
	p, _, _ = strings.Cut(p, "?")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return Where{Path: Resolve(p, dir)}, nil
}

// Resolve is path p read from dir: ~/ is the home folder, a relative
// path is dir's.
func Resolve(p, dir string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p)
}

// LoadImage reads and decodes the image at w, at most width pixels wide
// for an SVG.
func LoadImage(ctx context.Context, w Where, svgWidth int) (image.Image, error) {
	var b []byte
	var err error
	if w.URL != "" {
		b, err = fetch(ctx, w.URL)
	} else {
		b, err = readCapped(w.Path, MaxImageBytes)
	}
	if err != nil {
		return nil, err
	}
	return Decode(b, svgWidth)
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a file")
	} else if fi.Size() > limit {
		return nil, fmt.Errorf("larger than %d MiB", limit>>20)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// client fetches remote images: no cookies, a timeout, http(s) only on
// redirects too.
var client = &http.Client{
	Timeout: RemoteTimeout,
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			return errors.New("redirect to " + r.URL.Scheme)
		}
		return nil
	},
}

func fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pitwall")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	if resp.ContentLength > MaxRemoteBytes {
		return nil, fmt.Errorf("larger than %d MiB", MaxRemoteBytes>>20)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxRemoteBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxRemoteBytes {
		return nil, fmt.Errorf("larger than %d MiB", MaxRemoteBytes>>20)
	}
	return b, nil
}

var svgStart = regexp.MustCompile(`(?s)^\s*(<\?xml[^>]*>\s*)?(<!--.*?-->\s*)*(<!DOCTYPE[^>]*>\s*)?<svg\b`)

// Decode decodes PNG, JPEG, GIF (its first frame), WebP or SVG bytes,
// refusing more than MaxPixels and scaling the longest side down to
// MaxSide.
func Decode(b []byte, svgWidth int) (image.Image, error) {
	if svgStart.Match(b) {
		img := rasterSVG(b, svgWidth)
		if img == nil {
			return nil, errors.New("SVG not drawn")
		}
		return img, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if float64(cfg.Width)*float64(cfg.Height) > MaxPixels {
		return nil, fmt.Errorf("%dx%d is too many pixels", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return shrink(img, MaxSide), nil
}

// shrink scales img so its longest side is at most side.
func shrink(img image.Image, side int) image.Image {
	sz := img.Bounds().Size()
	if sz.X <= side && sz.Y <= side {
		return img
	}
	w, h := side, sz.Y*side/sz.X
	if sz.Y > sz.X {
		w, h = sz.X*side/sz.Y, side
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	return dst
}

var oneScale = regexp.MustCompile(`scale\(\s*([-+0-9.eE]+)\s*\)`)

// rasterSVG draws an SVG at its own size, scaled down to width, nil when
// oksvg cannot read it.
//
// Adapted from internal/ui/sidebar/favicon.go: decodeSVG, which draws a
// fixed square.
func rasterSVG(b []byte, width int) (img image.Image) {
	defer func() {
		if recover() != nil {
			img = nil
		}
	}()
	// workaround(oksvg be6e8873101c): it reads scale(s) as scale(s, 0).
	b = oneScale.ReplaceAll(b, []byte("scale(${1} ${1})"))
	icon, err := oksvg.ReadIconStream(bytes.NewReader(b), oksvg.IgnoreErrorMode)
	if err != nil || icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil
	}
	vb := icon.ViewBox
	w, h := vb.W, vb.H
	if ww, hh := svgSize(b); ww > 0 && hh > 0 {
		w, h = ww, hh
	}
	if width > 0 && w > float64(width) {
		w, h = float64(width), h*float64(width)/w
	}
	if w*h > MaxPixels || w < 1 || h < 1 {
		return nil
	}
	iw, ih := int(w+0.5), int(h+0.5)
	// workaround(oksvg be6e8873101c): SetTarget moves by the viewBox's
	// origin in pixels, not in its own units; scale after moving.
	icon.Transform = rasterx.Identity.Scale(w/vb.W, h/vb.H).Translate(-vb.X, -vb.Y)
	dst := image.NewRGBA(image.Rect(0, 0, iw, ih))
	icon.Draw(rasterx.NewDasher(iw, ih, rasterx.NewScannerGV(iw, ih, dst, dst.Bounds())), 1)
	return dst
}

var svgDims = regexp.MustCompile(`(?s)<svg\b[^>]*`)

// svgSize is the root element's width and height in pixels, 0 when it
// gives none or gives them in other units.
func svgSize(b []byte) (float64, float64) {
	t := string(svgDims.Find(b))
	num := func(name string) float64 {
		v := strings.TrimSuffix(attr(t, name), "px")
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err != nil || strings.TrimLeft(v, "0123456789.") != "" {
			return 0
		}
		return f
	}
	return num("width"), num("height")
}
