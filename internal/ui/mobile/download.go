package mobile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxAPKSize = 512 << 20

// apkTestURL permits the compiled emulator fixture's loopback origin.
var apkTestURL string

func validAPKURL(raw, local string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	if local != "" {
		fixture, err := url.Parse(local)
		if err == nil && fixture.Host == u.Host && fixture.Scheme == u.Scheme {
			ip := net.ParseIP(u.Hostname())
			if ip != nil && ip.IsLoopback() {
				return true
			}
		}
	}
	if u.Scheme != "https" || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "github.com":
		return strings.HasPrefix(u.Path, "/quanticstudios/pitwall/releases/download/")
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

func downloadAPK(ctx context.Context, c *http.Client, source string, size int64, cache, local string, progress func(int64)) (path string, err error) {
	if !validAPKURL(source, local) {
		return "", errors.New("APK URL must use HTTPS on a GitHub download host")
	}
	if size <= 0 || size > maxAPKSize {
		return "", errors.New("invalid APK asset size")
	}
	client := *c
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many APK redirects")
		}
		if !validAPKURL(req.URL.String(), local) {
			return errors.New("APK redirect is outside GitHub download hosts")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("APK download: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return "", errors.New("APK size does not match the release asset")
	}
	f, err := os.CreateTemp(cache, "pitwall-update-*.apk")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(name)
		}
	}()
	w := &apkProgress{Writer: f, total: size, report: progress}
	n, err := io.Copy(w, io.LimitReader(resp.Body, size+1))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("APK size mismatch: got %d bytes, expected %d", n, size)
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return "", err
	}
	return name, nil
}

type apkProgress struct {
	io.Writer
	written, total int64
	last           time.Time
	report         func(int64)
}

func (w *apkProgress) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.written += int64(n)
	if w.report != nil && (time.Since(w.last) >= 100*time.Millisecond || w.written >= w.total) {
		w.last = time.Now()
		w.report(min(w.written, w.total))
	}
	return n, err
}
