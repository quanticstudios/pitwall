package remote

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tailscale state values reported by the daemon in tailscale-status.json.
const (
	TailscaleOff        = "off"
	TailscaleStarting   = "starting"
	TailscaleNeedsLogin = "needs_login"
	TailscaleRunning    = "running"
	TailscaleStopping   = "stopping"
	TailscaleError      = "error"
)

// TailscaleStatus is the daemon's current built-in Tailscale state,
// written atomically to remote/tailscale-status.json so `pitwall remote
// tailscale status` and Settings, Phone can read it without a separate RPC.
type TailscaleStatus struct {
	Enabled   bool      `json:"enabled"`
	State     string    `json:"state"`
	Hostname  string    `json:"hostname,omitempty"`
	DNSName   string    `json:"dns_name,omitempty"`
	URL       string    `json:"url,omitempty"`
	AuthURL   string    `json:"auth_url,omitempty"`
	SelfUser  string    `json:"self_user,omitempty"`
	IPs       []string  `json:"ips,omitempty"`
	Error     string    `json:"error,omitempty"`
	PID       int       `json:"pid,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TailscaleDir is the isolated tsnet state directory inside dir (mode 0700).
func TailscaleDir(dir string) string { return filepath.Join(dir, "tsnet") }

// TailscaleStatusPath is the status file path inside dir.
func TailscaleStatusPath(dir string) string { return filepath.Join(dir, "tailscale-status.json") }

// TailscalePurgePath is the marker file asking the daemon to delete
// TailscaleDir after logging out and closing tsnet.
func TailscalePurgePath(dir string) string { return filepath.Join(dir, "tailscale-purge") }

// WriteTailscaleStatus writes st to dir/tailscale-status.json at mode 0600.
func WriteTailscaleStatus(dir string, st TailscaleStatus) error {
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	return writeJSON(TailscaleStatusPath(dir), st)
}

// ReadTailscaleStatus reads dir/tailscale-status.json.
func ReadTailscaleStatus(dir string) (TailscaleStatus, bool) {
	var st TailscaleStatus
	if readJSON(TailscaleStatusPath(dir), &st) != nil {
		return TailscaleStatus{}, false
	}
	return st, true
}

// TailscaleStateExists reports whether TailscaleDir holds saved node state.
func TailscaleStateExists(dir string) bool {
	es, err := os.ReadDir(TailscaleDir(dir))
	return err == nil && len(es) > 0
}

// RequestTailscalePurge records a request to delete TailscaleDir on stop.
func RequestTailscalePurge(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(TailscalePurgePath(dir), []byte("purge\n"), 0o600)
}

// ConsumeTailscalePurge reports whether a purge was requested and clears the marker.
func ConsumeTailscalePurge(dir string) bool {
	err := os.Remove(TailscalePurgePath(dir))
	return err == nil
}

// RemoveTailscaleState deletes the tsnet state dir, any pending purge marker,
// and any Tailscale push subscriptions (devices/ts-*.push.json).
func RemoveTailscaleState(dir string) error {
	_ = os.Remove(TailscalePurgePath(dir))
	if err := os.RemoveAll(TailscaleDir(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if es, err := os.ReadDir(filepath.Join(dir, "devices")); err == nil {
		for _, e := range es {
			if strings.HasPrefix(e.Name(), "ts-") && strings.HasSuffix(e.Name(), ".push.json") {
				_ = os.Remove(filepath.Join(dir, "devices", e.Name()))
			}
		}
	}
	return nil
}
