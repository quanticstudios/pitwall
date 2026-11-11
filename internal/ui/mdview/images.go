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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/quanticstudios/pitwall/internal/repoicon"
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
func ResolveImage(src, dir string) (Where, error) { return resolveImage(src, dir, true) }

// resolveImage is ResolveImage, with ~/ the home folder only when home
// is set.
func resolveImage(src, dir string, home bool) (Where, error) {
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
	return Where{Path: resolve(p, dir, home)}, nil
}

// Resolve is path p read from dir: ~/ is the home folder, a relative
// path is dir's.
func Resolve(p, dir string) string { return resolve(p, dir, true) }

// resolve is Resolve; without home, ~/ stays for the computer to read.
func resolve(p, dir string, home bool) string {
	if !home {
		// why: the path is the computer's to read, so it keeps slashes
		// whichever system draws the reader.
		if !path.IsAbs(p) && !strings.HasPrefix(p, "~/") && p != "~" {
			p = path.Join(filepath.ToSlash(dir), p)
		}
		return path.Clean(p)
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, rest)
		}
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		return filepath.Clean(p)
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

// readCapped reads the regular file at path, up to limit bytes. A FIFO
// or a device is refused before it is opened, as os.Open would wait for
// a FIFO's writer; the open does not wait either, and the type is checked
// again on what it opened, in case the path changed in between.
func readCapped(path string, limit int64) ([]byte, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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

// maxRedirects is how many redirects a download follows.
const maxRedirects = 3

// dialable reports whether a download may connect to addr. Tests let
// loopback through.
var dialable = publicAddr

// notPublic are ranges publicAddr refuses beyond the ones netip names:
// shared address space, which holds the tailnet's 100.x addresses, this
// network, IETF protocol assignments, benchmarking, the reserved block,
// and the tailnet's IPv6 range.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// nat64 is the well-known NAT64 prefix; its last 32 bits are an IPv4
// address, checked as one.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// publicAddr reports whether a is an address on the internet: not
// loopback, private, link-local (cloud metadata services among them),
// multicast, unspecified, or in notPublic.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// dialer checks the address it connects to after DNS has answered, so a
// name that resolves to a private address, a rebinding name, or a
// redirect to one does not get through.
var dialer = &net.Dialer{
	Timeout: RemoteTimeout,
	Control: func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if !dialable(ap.Addr()) {
			return fmt.Errorf("%s is not a public address", ap.Addr())
		}
		return nil
	},
}

// client fetches remote images: no proxy, which would connect for it,
// no cookies, a timeout, public addresses only, http(s) only on
// redirects too.
var client = &http.Client{
	Timeout:   RemoteTimeout,
	Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: RemoteTimeout, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second},
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
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
	if repoicon.SVGSafe(b) != nil {
		return nil
	}
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
	if s := float64(MaxSide) / max(w, h); s < 1 {
		w, h = w*s, h*s
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
