//go:build linux

package config

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestParsePortalColorScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  any
		want ColorScheme
	}{
		{"dark uint32", uint32(1), SchemeDark},
		{"light uint32", uint32(2), SchemeLight},
		{"no preference", uint32(0), SchemeUnknown},
		{"variant dark", dbus.MakeVariant(uint32(1)), SchemeDark},
		{"nested variant light", dbus.MakeVariant(dbus.MakeVariant(uint32(2))), SchemeLight},
		{"int32 light", int32(2), SchemeLight},
		{"wrong type", "dark", SchemeUnknown},
		{"out of range", uint32(9), SchemeUnknown},
	} {
		if got := parsePortalColorScheme(tc.val); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWatchPortalSignals(t *testing.T) {
	stop := make(chan struct{})
	signals := make(chan *dbus.Signal, 4)
	fired := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchPortalSignals(stop, signals, func() { fired <- struct{}{} })
	}()

	// Unrelated namespace/key should not trigger.
	signals <- &dbus.Signal{Body: []any{"org.freedesktop.appearance", "accent-color", dbus.MakeVariant(uint32(1))}}
	signals <- &dbus.Signal{Body: []any{"org.other.ns", "color-scheme", dbus.MakeVariant(uint32(2))}}
	select {
	case <-fired:
		t.Fatal("unrelated signal fired changed callback")
	case <-time.After(120 * time.Millisecond):
	}

	// Matching color-scheme signal triggers once after debounce.
	signals <- &dbus.Signal{Body: []any{portalNamespace, portalKey, dbus.MakeVariant(uint32(2))}}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for portal color-scheme callback")
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchPortalSignals did not exit on stop")
	}
}
