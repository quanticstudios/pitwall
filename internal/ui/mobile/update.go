package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/update"
)

var releaseURL = "https://api.github.com/repos/quanticstudios/pitwall/releases/latest"

type phoneSettings struct {
	CheckUpdates bool   `json:"check_updates"`
	Dismissed    string `json:"dismissed,omitempty"`
}

func loadPhoneSettings(dir string) (phoneSettings, error) {
	s := phoneSettings{CheckUpdates: true}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func savePhoneSettings(dir string, s phoneSettings) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "settings.json"))
}

type phoneRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r phoneRelease) apkURL() string {
	for _, a := range r.Assets {
		if a.Name == "pitwall-phone.apk" {
			u, err := url.Parse(a.URL)
			if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
				return a.URL
			}
		}
	}
	return ""
}

var phoneDescribe = regexp.MustCompile(`^(v\d+\.\d+\.\d+(?:-[A-Za-z]+\.\d+)?)(?:-\d+-g[0-9a-f]+(?:-dirty)?)?$`)

func phoneVersion(version string) string {
	v := "v" + strings.TrimPrefix(version, "v")
	if m := phoneDescribe.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

func (r phoneRelease) offered(current string, s phoneSettings) bool {
	return s.CheckUpdates && r.apkURL() != "" && update.Newer(r.Tag, phoneVersion(current)) &&
		(s.Dismissed == "" || update.Newer(r.Tag, s.Dismissed))
}

func fetchPhoneRelease(ctx context.Context, c *http.Client, endpoint, etag string, cached phoneRelease) (phoneRelease, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cached, etag, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.Do(req)
	if err != nil {
		return cached, etag, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && etag != "" {
		return cached, etag, nil
	}
	if resp.StatusCode != http.StatusOK {
		return cached, etag, fmt.Errorf("GitHub release: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return cached, etag, err
	}
	if len(b) > 1<<20 {
		return cached, etag, errors.New("GitHub release exceeds 1 MiB")
	}
	var r phoneRelease
	if err := json.Unmarshal(b, &r); err != nil {
		return cached, etag, errors.New("invalid GitHub release JSON")
	}
	return r, resp.Header.Get("ETag"), nil
}

type phoneUpdates struct {
	settings              phoneSettings
	release               phoneRelease
	etag                  string
	next                  time.Time
	cancel                context.CancelFunc
	request               int
	open, dismiss, toggle widget.Clickable
}

func (a *App) pollUpdates(gtx C) {
	u := &a.updates
	if !a.focused || !u.settings.CheckUpdates || phoneVersion(Version) == "" {
		return
	}
	if !gtx.Now.Before(u.next) && u.cancel == nil {
		u.next = gtx.Now.Add(24 * time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		u.cancel = cancel
		u.request++
		request, etag, cached := u.request, u.etag, u.release
		go func() {
			r, etag, err := fetchPhoneRelease(ctx, &http.Client{Timeout: 10 * time.Second}, releaseURL, etag, cached)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("mobile: update check: %v", err)
			}
			done := func() {
				if u.request != request {
					return
				}
				u.cancel = nil
				if err == nil && u.settings.CheckUpdates {
					u.release, u.etag = r, etag
				}
			}
			select {
			case a.ui <- done:
				a.w.Invalidate()
			case <-a.closed:
			}
		}()
	}
	gtx.Execute(op.InvalidateCmd{At: u.next})
}

func (a *App) saveUpdates(s phoneSettings) bool {
	if err := savePhoneSettings(a.dir, s); err != nil {
		a.mach.note = "Could not save update settings."
		log.Printf("mobile: update settings: %v", err)
		return false
	}
	a.updates.settings = s
	return true
}

func (a *App) updateLine(gtx C) D {
	u, th := &a.updates, a.th
	if !u.release.offered(Version, u.settings) {
		return D{}
	}
	if u.dismiss.Clicked(gtx) {
		s := u.settings
		s.Dismissed = u.release.Tag
		if a.saveUpdates(s) {
			return D{}
		}
	}
	if u.open.Clicked(gtx) {
		if err := openUpdateURL(u.release.apkURL()); err != nil {
			log.Printf("mobile: open update: %v", err)
		}
	}
	return layout.Inset{Left: pad, Right: 2}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D { return small(gtx, th, th.Fg, "pitwall "+u.release.Tag+" is out") }),
			layout.Rigid(func(gtx C) D { return textButton(gtx, th, &u.open, "Update") }),
			layout.Rigid(func(gtx C) D {
				semantic.DescriptionOp("Dismiss this version").Add(gtx.Ops)
				return iconButton(gtx, th, &u.dismiss, icon("x"))
			}),
		)
	})
}

func (a *App) updateToggle(gtx C) D {
	u, th := &a.updates, a.th
	if u.toggle.Clicked(gtx) {
		s := u.settings
		s.CheckUpdates = !s.CheckUpdates
		if a.saveUpdates(s) {
			if u.cancel != nil {
				u.cancel()
				u.cancel = nil
				u.request++
			}
			u.next = time.Time{}
			gtx.Execute(op.InvalidateCmd{})
		}
	}
	return menuRow(gtx, th, &u.toggle, false, func(gtx C) D {
		semantic.CheckBox.Add(gtx.Ops)
		semantic.SelectedOp(u.settings.CheckUpdates).Add(gtx.Ops)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D { return body(gtx, th, th.Fg, "Check for updates") }),
			layout.Rigid(func(gtx C) D {
				name, col := "x", th.Muted
				if u.settings.CheckUpdates {
					name, col = "check", th.Primary
				}
				return sidebar.Icon(gtx, name, gtx.Dp(18), col)
			}),
		)
	})
}
