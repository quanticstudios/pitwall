//go:build linux

package config

import (
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest      = "org.freedesktop.portal.Desktop"
	portalPath      = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalSettings  = "org.freedesktop.portal.Settings"
	portalNamespace = "org.freedesktop.appearance"
	portalKey       = "color-scheme"
	portalDebounce  = 50 * time.Millisecond
)

func detectSystemColorScheme() ColorScheme {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return SchemeUnknown
	}
	defer conn.Close()
	obj := conn.Object(portalDest, portalPath)
	var v dbus.Variant
	if err := obj.Call(portalSettings+".ReadOne", 0, portalNamespace, portalKey).Store(&v); err == nil {
		return parsePortalColorScheme(v)
	}
	if err := obj.Call(portalSettings+".Read", 0, portalNamespace, portalKey).Store(&v); err == nil {
		return parsePortalColorScheme(v)
	}
	return SchemeUnknown
}

// parsePortalColorScheme decodes the XDG Desktop Portal color-scheme value:
// 1 is prefer-dark, 2 is prefer-light, 0 is no preference. Older portal
// versions wrap Read's reply in a nested dbus.Variant.
func parsePortalColorScheme(v any) ColorScheme {
	for {
		vr, ok := v.(dbus.Variant)
		if !ok {
			break
		}
		v = vr.Value()
	}
	var n uint32
	switch x := v.(type) {
	case uint32:
		n = x
	case int32:
		if x >= 0 {
			n = uint32(x)
		}
	case uint64:
		n = uint32(x)
	case int64:
		if x >= 0 {
			n = uint32(x)
		}
	case uint:
		n = uint32(x)
	case int:
		if x >= 0 {
			n = uint32(x)
		}
	default:
		return SchemeUnknown
	}
	switch n {
	case 1:
		return SchemeDark
	case 2:
		return SchemeLight
	default:
		return SchemeUnknown
	}
}

// WatchSystemAppearance calls changed whenever the XDG desktop portal reports
// a new color-scheme setting, until stop closes. On sessions without D-Bus it
// returns immediately.
func WatchSystemAppearance(stop <-chan struct{}, changed func()) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return
	}
	defer conn.Close()
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(portalPath),
		dbus.WithMatchInterface(portalSettings),
		dbus.WithMatchMember("SettingChanged"),
	); err != nil {
		return
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	watchPortalSignals(stop, signals, changed)
}

func watchPortalSignals(stop <-chan struct{}, signals <-chan *dbus.Signal, changed func()) {
	var fire <-chan time.Time
	for {
		select {
		case <-stop:
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if isPortalColorSchemeSignal(sig) {
				fire = time.After(portalDebounce)
			}
		case <-fire:
			fire = nil
			changed()
		}
	}
}

func isPortalColorSchemeSignal(sig *dbus.Signal) bool {
	if sig == nil || len(sig.Body) < 3 {
		return false
	}
	ns, ok1 := sig.Body[0].(string)
	key, ok2 := sig.Body[1].(string)
	return ok1 && ok2 && ns == portalNamespace && key == portalKey
}
