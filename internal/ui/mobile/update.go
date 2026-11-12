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
	"sync/atomic"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/update"
)

var releaseURL = "https://api.github.com/repos/quanticstudios/pitwall/releases/latest"

var errUpdateRateLimit = errors.New("GitHub's rate limit was reached. Try again in an hour.")

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
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return cached, etag, errUpdateRateLimit
		}
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
	progress                        atomic.Int64
	downloadSize                    int64
	sweptCache                      string
	downloadCancel                  context.CancelFunc
}

type installState struct {
	cache, message string
	busy           bool
}

func (a *App) applyInstallState(s installState) {
	u := &a.updates
	u.cache = s.cache
	if u.downloadCancel == nil || s.busy || s.message != "" {
		u.installNote, u.installing = s.message, s.busy
	}
	if s.message == "Update installed." && u.checked {
		u.installNote = ""
	}
	if !s.busy && u.downloadCancel == nil && s.cache != "" && u.sweptCache != s.cache {
		if err := sweepAPKs(s.cache); err != nil {
			log.Printf("mobile: APK cache: %v", err)
		}
		u.sweptCache = s.cache
	}
}

func sweepAPKs(cache string) error {
	files, err := filepath.Glob(filepath.Join(cache, "pitwall-update-*.apk"))
	for _, f := range files {
		err = errors.Join(err, os.Remove(f))
	}
	return err
}

func (a *App) reportDownloadProgress(n int64) {
	a.updates.progress.Store(n)
	a.w.Invalidate()
}

func (u *phoneUpdates) canInstall() bool { return !u.installing && u.release.available(Version) }

func (u *phoneUpdates) lineText() string {
	if u.installNote != "" {
		return u.installNote
	}
	return "pitwall " + u.release.Tag + " is out"
}

func (u *phoneUpdates) checkText() string {
	switch {
	case u.installNote != "":
		return u.installNote
	case u.cancel != nil:
		return "Checking…"
	case u.checkError != "":
		return u.checkError
	case u.release.available(Version):
		return u.release.Tag + " is out"
	case update.Newer(u.release.Tag, phoneVersion(Version)):
		return u.release.Tag + " is out; its APK is not attached yet"
	case u.checked:
		return "Up to date (" + Version + ")"
	}
	return "Installed " + Version
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
				if !u.installing {
					u.installNote = ""
				}
			} else if err != nil {
				u.checkError = "Could not check for updates: " + err.Error()
				if errors.Is(err, errUpdateRateLimit) {
					u.checkError = err.Error()
				}
			}
		}
		a.postUpdate(done)
	}()
}

func (a *App) startInstall() {
	u := &a.updates
	if !u.canInstall() {
		return
	}
	if u.cache == "" {
		u.installNote = "Could not reach Android's installer. Close and reopen pitwall."
		return
	}
	u.installing, u.installNote = true, "Downloading update…"
	u.progress.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	u.downloadCancel = cancel
	url, size, cache := u.release.apkURL(), u.release.apkSize(), u.cache
	u.downloadSize = size
	go func() {
		path, err := downloadAPK(ctx, &http.Client{Timeout: 10 * time.Minute}, url, size, cache, apkTestURL, a.reportDownloadProgress)
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
		if u.installNote != "" {
			u.installNote = ""
			return D{}
		}
		s := u.settings
		s.Dismissed = u.release.Tag
		if a.saveUpdates(s) {
			return D{}
		}
	}
	if u.install.Clicked(gtx) {
		a.startInstall()
	}
	note := u.lineText()
	return layout.Inset{Left: pad, Right: 2}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, 1, note) }),
			layout.Rigid(func(gtx C) D {
				if !u.canInstall() {
					return D{}
				}
				return textButton(gtx, th, &u.install, "Install")
			}),
			layout.Rigid(func(gtx C) D {
				if u.installing {
					return D{}
				}
				description := "Dismiss this version"
				if u.installNote != "" {
					description = "Clear update status"
				}
				semantic.DescriptionOp(description).Add(gtx.Ops)
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
	note := u.checkText()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return kit.WideButton(gtx, th, &u.check, kit.Secondary.When(u.cancel == nil && !u.installing), kit.Large, "Check for updates")
		}),
		layout.Rigid(gap(8)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D { return small(gtx, th, th.Fg, note) }),
				layout.Rigid(func(gtx C) D {
					if !u.canInstall() {
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
	progress := float32(u.progress.Load()) / float32(u.downloadSize)
	semantic.DescriptionOp(fmt.Sprintf("%d percent", int(progress*100))).Add(gtx.Ops)
	paint.FillShape(gtx.Ops, a.th.Border, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, a.th.Primary, clip.Rect{Max: image.Pt(int(float32(size.X)*progress), size.Y)}.Op())
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
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D { return body(gtx, th, th.Fg, "Check daily") }),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.Y = gtx.Dp(touch)
			return kit.Switch(gtx, th, &u.toggle, u.settings.CheckUpdates)
		}),
	)
}
