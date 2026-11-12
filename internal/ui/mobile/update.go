package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
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
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (r phoneRelease) apkURL() string {
	for _, a := range r.Assets {
		if a.Name == "pitwall-phone.apk" && validAPKURL(a.URL, apkTestURL) && a.Size > 0 && a.Size <= maxAPKSize {
			return a.URL
		}
	}
	return ""
}

func (r phoneRelease) apkSize() int64 {
	for _, a := range r.Assets {
		if a.Name == "pitwall-phone.apk" {
			return a.Size
		}
	}
	return 0
}

func (r phoneRelease) available(current string) bool {
	return r.apkURL() != "" && update.Newer(r.Tag, phoneVersion(current))
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
	return s.CheckUpdates && r.available(current) &&
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
	settings                        phoneSettings
	release                         phoneRelease
	etag                            string
	next                            time.Time
	cancel                          context.CancelFunc
	request                         int
	install, dismiss, toggle, check widget.Clickable
	checked                         bool
	checkError                      string
	cache, installNote              string
	installing                      bool
	progress                        float32
	downloadCancel                  context.CancelFunc
}

func (a *App) pollUpdates(gtx C) {
	u := &a.updates
	if !a.focused || !u.settings.CheckUpdates || phoneVersion(Version) == "" {
		return
	}
	if !gtx.Now.Before(u.next) && u.cancel == nil {
		a.startUpdateCheck(gtx.Now, false)
	}
	gtx.Execute(op.InvalidateCmd{At: u.next})
}

func (a *App) postUpdate(f func()) {
	select {
	case a.ui <- f:
		a.w.Invalidate()
	case <-a.closed:
	}
}

func (a *App) startUpdateCheck(now time.Time, manual bool) {
	u := &a.updates
	if u.cancel != nil {
		return
	}
	u.next = now.Add(24 * time.Hour)
	u.checkError = ""
	if manual && !u.installing {
		u.installNote = ""
	}
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
			if err == nil && (manual || u.settings.CheckUpdates) {
				u.release, u.etag = r, etag
				u.checked = true
			} else if err != nil {
				u.checkError = "Could not check for updates: " + err.Error()
			}
		}
		a.postUpdate(done)
	}()
}

func (a *App) startInstall() {
	u := &a.updates
	if u.installing || !u.release.available(Version) {
		return
	}
	if u.cache == "" {
		u.installNote = "Android's installer is not ready."
		return
	}
	u.installing, u.progress, u.installNote = true, 0, "Downloading update…"
	ctx, cancel := context.WithCancel(context.Background())
	u.downloadCancel = cancel
	url, size, cache := u.release.apkURL(), u.release.apkSize(), u.cache
	go func() {
		path, err := downloadAPK(ctx, &http.Client{Timeout: 10 * time.Minute}, url, size, cache, apkTestURL, func(n int64) {
			a.postUpdate(func() { u.progress = float32(n) / float32(size) })
		})
		cancel()
		if err == nil {
			err = installAPK(path)
		}
		if err != nil && path != "" {
			os.Remove(path)
		}
		a.postUpdate(func() {
			u.downloadCancel = nil
			if err != nil {
				u.installing, u.installNote = false, "Update failed: "+err.Error()
			}
		})
	}()
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
	if !u.installing && u.installNote == "" && !u.release.offered(Version, u.settings) {
		return D{}
	}
	if u.dismiss.Clicked(gtx) && !u.installing {
		s := u.settings
		s.Dismissed = u.release.Tag
		if a.saveUpdates(s) {
			u.installNote = ""
			return D{}
		}
	}
	if u.install.Clicked(gtx) {
		a.startInstall()
	}
	note := "pitwall " + u.release.Tag + " is out"
	if u.installNote != "" {
		note = u.installNote
	}
	return layout.Inset{Left: pad, Right: 2}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, 1, note) }),
			layout.Rigid(func(gtx C) D {
				if u.installing || !u.release.available(Version) {
					return D{}
				}
				return textButton(gtx, th, &u.install, "Install")
			}),
			layout.Rigid(func(gtx C) D {
				semantic.DescriptionOp("Dismiss this version").Add(gtx.Ops)
				return iconButton(gtx, th, &u.dismiss, icon("x"))
			}),
		)
	})
}

func (a *App) updateSettings(gtx C) D {
	u, th := &a.updates, a.th
	if u.check.Clicked(gtx) {
		a.startUpdateCheck(gtx.Now, true)
	}
	if u.install.Clicked(gtx) {
		a.startInstall()
	}
	note := "Installed " + Version
	switch {
	case u.cancel != nil:
		note = "Checking"
	case u.checkError != "":
		note = u.checkError
	case u.release.available(Version):
		note = u.release.Tag + " is out"
	case u.checked:
		note = "Up to date (" + Version + ")"
	}
	if u.installNote != "" {
		note = u.installNote
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return kit.WideButton(gtx, th, &u.check, kit.Secondary.When(u.cancel == nil && !u.installing), kit.Large, "Check for updates")
		}),
		layout.Rigid(gap(8)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D { return small(gtx, th, th.Fg, note) }),
				layout.Rigid(func(gtx C) D {
					if u.installing || !u.release.available(Version) {
						return D{}
					}
					return textButton(gtx, th, &u.install, "Install")
				}),
			)
		}),
		layout.Rigid(a.updateProgress),
	)
}

func (a *App) updateProgress(gtx C) D {
	u := &a.updates
	if u.downloadCancel == nil {
		return D{}
	}
	size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(4))
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	semantic.LabelOp("Update download").Add(gtx.Ops)
	semantic.DescriptionOp(fmt.Sprintf("%d percent", int(u.progress*100))).Add(gtx.Ops)
	paint.FillShape(gtx.Ops, a.th.Border, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, a.th.Primary, clip.Rect{Max: image.Pt(int(float32(size.X)*u.progress), size.Y)}.Op())
	return D{Size: size}
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
			layout.Flexed(1, func(gtx C) D { return body(gtx, th, th.Fg, "Check daily") }),
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
