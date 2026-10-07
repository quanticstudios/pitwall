package app

import (
	"gioui.org/io/key"
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/settings"
)

// openSettings shows the settings page in place of the active tab's panes.
func (u *ui) openSettings() {
	u.settingsWS = u.nav.workspace
	path := u.cfg.Path
	if path == "" {
		path = config.Path()
	}
	u.settings.Version = Version
	u.settings.Show(path)
}

// settingsKeys lets the page take its keys before the window's shortcuts:
// a chord being recorded, and Escape. An open switcher keeps Escape.
func (u *ui) settingsKeys(gtx gl.Context) {
	if !u.nav.switcherVisible() {
		u.settingsResult(u.settings.Keys(gtx))
	}
}

// settingsShortcut handles open_settings, which opens and closes the page.
func (u *ui) settingsShortcut(e key.Event) bool {
	if u.nav.tabMode || u.nav.paneMode || u.nav.bind().Action(e) != "open_settings" {
		return false
	}
	if e.State == key.Press {
		if u.settings.Shown() {
			u.settings.Hide()
		} else {
			u.openSettings()
		}
	}
	return true
}

// layoutSettings draws the page, or the panes again once another tab is
// selected (from the keyboard, say).
func (u *ui) layoutSettings(gtx gl.Context, st *model.State) {
	if u.nav.workspace != u.settingsWS {
		u.settings.Hide()
		u.layoutPanes(gtx, st)
		return
	}
	u.settings.SetDecisions(st.Decide)
	u.settingsResult(u.settings.Layout(gtx, u.th, u.cfg, u.probs))
}

func (u *ui) settingsResult(r settings.Result) {
	switch r {
	case settings.Closed:
		u.settings.Hide()
	case settings.Saved:
		u.reloadConfig()
	}
}

// reloadConfig applies config.toml now, after the page wrote it, rather
// than when the watcher next polls.
func (u *ui) reloadConfig() {
	l := loadConfig()
	u.cfgMu.Lock()
	u.watchTheme = l.s.ThemeName
	u.cfgMu.Unlock()
	u.apply(l)
	if u.report != nil {
		u.report(l.probs)
	}
}
