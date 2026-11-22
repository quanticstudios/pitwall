package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/qr"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/tsnet"
)

const remoteUsage = `usage:
  pitwall remote pair [name]               show a QR code that pairs a phone, good for 10 minutes
  pitwall remote devices                   list paired devices
  pitwall remote revoke <name>             unpair a device, by name or id
  pitwall remote tailscale on [--hostname name]
                                           join your tailnet inside pitwall and serve the phone page over HTTPS
  pitwall remote tailscale off [--delete-state]
                                           leave the tailnet (logout); --delete-state removes saved node state
  pitwall remote tailscale status [--json] show built-in Tailscale state, URL and allowed identities
Turn the phone page on with [remote] enabled = true in config.toml, or use
pitwall remote tailscale on; see docs/phone.md.
`

const remoteTailscaleUsage = `usage:
  pitwall remote tailscale on [--hostname name]
  pitwall remote tailscale off [--delete-state]
  pitwall remote tailscale status [--json]
`

// Hooks overridden in unit tests.
var (
	remoteDaemonRunning = isDaemonRunning
	remoteTailscaleWait = 25 * time.Second
	remoteTailscalePoll = 150 * time.Millisecond
	remoteLogoutDir     = func(ctx context.Context, dir, hostname string) error {
		return tsnet.LogoutDir(ctx, dir, hostname, "")
	}
)

func isDaemonRunning() bool {
	path, err := socketPath()
	if err != nil {
		return false
	}
	nc, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return false
	}
	nc.Close()
	return true
}

// runRemote is `pitwall remote <cmd>`; it returns the exit status.
func runRemote(args []string, stdout, stderr io.Writer) int {
	dir := remote.Dir()
	fail := func(err error) int {
		fmt.Fprintln(stderr, "pitwall:", err)
		return 1
	}
	switch {
	case len(args) >= 1 && args[0] == "pair":
		s, _ := config.Load()
		if !s.Remote.Enabled {
			fmt.Fprintf(stderr, "pitwall: the phone page is off; set enabled = true under [remote] in %s, or turn it on in Settings, Phone\n", config.Path())
			return 1
		}
		code, err := remote.Pair(dir, strings.Join(args[1:], " "), time.Now())
		if err != nil {
			return fail(err)
		}
		url := remote.PairURL(s.Remote, code)
		c, err := qr.Encode(url)
		if err != nil {
			return fail(err)
		}
		fmt.Fprint(stdout, c)
		fmt.Fprintf(stdout, "\nScan this with your phone, or open:\n  %s\nor enter the code %s on the page.\n\nIt pairs %q once, within %.0f minutes.\n", url, code, remote.PairName(dir, time.Now()), remote.PairTTL.Minutes())
		if s.Remote.TLS {
			if _, fp, err := remote.Cert(dir); err == nil {
				fmt.Fprintf(stdout, "The page's certificate is pitwall's own. Before you trust it, check its SHA-256 is\n  %s\n", fp)
			}
		}
		if host, port, _ := net.SplitHostPort(s.Remote.Listen); s.Remote.URL == "" && config.Loopback(host) {
			fmt.Fprintf(stdout, "\nThat address is this computer's own. Put the page on your tailnet with\n  pitwall remote tailscale on\nor\n  tailscale serve --bg %s\nand set url under [remote] to the https address it prints, then pair again.\n", port)
		}
	case len(args) == 1 && args[0] == "devices":
		devs, err := remote.Devices(dir)
		if err != nil {
			return fail(err)
		}
		if len(devs) == 0 {
			fmt.Fprintln(stdout, "No paired devices. pitwall remote pair pairs one.")
		}
		for _, d := range devs {
			fmt.Fprintf(stdout, "%s  %-20s  paired %s\n", d.ID, d.Name, d.Paired.Local().Format("2006-01-02 15:04"))
		}
	case len(args) == 2 && args[0] == "revoke":
		d, err := remote.Revoke(dir, args[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Unpaired %q; its token no longer works.\n", d.Name)
	case len(args) >= 1 && args[0] == "tailscale":
		return runRemoteTailscale(args[1:], dir, stdout, stderr)
	default:
		fmt.Fprint(stderr, remoteUsage)
		return 2
	}
	return 0
}

func runRemoteTailscale(args []string, dir string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, remoteTailscaleUsage)
		return 2
	}
	switch args[0] {
	case "on":
		return runRemoteTailscaleOn(args[1:], dir, stdout, stderr)
	case "off":
		return runRemoteTailscaleOff(args[1:], dir, stdout, stderr)
	case "status":
		return runRemoteTailscaleStatus(args[1:], dir, stdout, stderr)
	default:
		fmt.Fprint(stderr, remoteTailscaleUsage)
		return 2
	}
}

func runRemoteTailscaleOn(args []string, dir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote tailscale on", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hostname := fs.String("hostname", "", "Tailscale device name")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(stderr, remoteTailscaleUsage)
		return 2
	}
	cfgPath := config.Path()
	if *hostname != "" {
		if err := config.SetKey(cfgPath, "remote.tailscale", "hostname", config.Quote(*hostname)); err != nil {
			fmt.Fprintln(stderr, "pitwall:", err)
			return 1
		}
	}
	if err := config.SetKey(cfgPath, "remote.tailscale", "enabled", "true"); err != nil {
		fmt.Fprintln(stderr, "pitwall:", err)
		return 1
	}
	s, probs := config.LoadFile(cfgPath)
	for _, p := range probs {
		if strings.Contains(p.Msg, "DNS label") || strings.Contains(p.String(), "remote.tailscale") {
			fmt.Fprintln(stderr, "pitwall:", p)
		}
	}
	if !s.Remote.Tailscale.Enabled {
		return 1
	}
	tsCfg := s.Remote.Tailscale
	if !remoteDaemonRunning() {
		fmt.Fprintf(stdout, "Enabled built-in Tailscale (device %q) in %s.\n", tsCfg.Hostname, cfgPath)
		fmt.Fprintln(stdout, "Start pitwall (or run `pitwall daemon`) to join your tailnet, then run `pitwall remote tailscale status` for the login or HTTPS URL.")
		return 0
	}

	since := time.Now().Add(-time.Second)
	deadline := time.Now().Add(remoteTailscaleWait)
	for {
		if st, ok := remote.ReadTailscaleStatus(dir); ok && st.Enabled {
			fresh := !st.UpdatedAt.Before(since) || st.State == remote.TailscaleRunning || (st.State == remote.TailscaleNeedsLogin && st.AuthURL != "")
			if fresh {
				switch st.State {
				case remote.TailscaleNeedsLogin:
					if st.AuthURL != "" {
						fmt.Fprintf(stdout, "Tailscale login required for %s.\nOpen this URL in a browser to join your tailnet:\n  %s\n\nFor headless setup, set PITWALL_TAILSCALE_AUTHKEY (or TS_AUTHKEY) in the daemon's environment, or auth_key under [remote.tailscale] in %s.\nRun `pitwall remote tailscale status` once logged in.\n", st.Hostname, st.AuthURL, cfgPath)
						return 0
					}
				case remote.TailscaleRunning:
					fmt.Fprintf(stdout, "Tailscale is on: %s\n", st.URL)
					if len(st.IPs) > 0 {
						fmt.Fprintf(stdout, "Device: %s (%s)\n", st.Hostname, strings.Join(st.IPs, ", "))
					}
					fmt.Fprintf(stdout, "Access: %s\n", formatTailscaleAccess(st.SelfUser, tsCfg.AllowUsers, tsCfg.AllowTags))
					return 0
				case remote.TailscaleError:
					if !st.UpdatedAt.Before(since) {
						fmt.Fprintf(stderr, "pitwall: tailscale: %s\n", st.Error)
						return 1
					}
				}
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stdout, "Enabled built-in Tailscale (device %q); still starting. Run `pitwall remote tailscale status` in a moment.\n", tsCfg.Hostname)
			return 0
		}
		time.Sleep(remoteTailscalePoll)
	}
}

func runRemoteTailscaleOff(args []string, dir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote tailscale off", flag.ContinueOnError)
	fs.SetOutput(stderr)
	deleteState := fs.Bool("delete-state", false, "delete saved Tailscale node state after logout")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(stderr, remoteTailscaleUsage)
		return 2
	}
	cfgPath := config.Path()
	s, _ := config.LoadFile(cfgPath)
	if *deleteState {
		if err := remote.RequestTailscalePurge(dir); err != nil {
			fmt.Fprintln(stderr, "pitwall:", err)
			return 1
		}
	}
	if err := config.SetKey(cfgPath, "remote.tailscale", "enabled", "false"); err != nil {
		fmt.Fprintln(stderr, "pitwall:", err)
		return 1
	}

	if remoteDaemonRunning() {
		st, ok := remote.ReadTailscaleStatus(dir)
		if ok && st.State != "" && st.State != remote.TailscaleOff {
			deadline := time.Now().Add(remoteTailscaleWait)
			for time.Now().Before(deadline) {
				cur, ok := remote.ReadTailscaleStatus(dir)
				if !ok || cur.State == remote.TailscaleOff {
					break
				}
				time.Sleep(remoteTailscalePoll)
			}
		}
		if *deleteState && remote.TailscaleStateExists(dir) {
			if err := remote.RemoveTailscaleState(dir); err != nil {
				fmt.Fprintln(stderr, "pitwall:", err)
				return 1
			}
		}
		if *deleteState {
			fmt.Fprintln(stdout, "Built-in Tailscale is off (logged out) and its saved state was deleted.")
		} else {
			fmt.Fprintln(stdout, "Built-in Tailscale is off (logged out). Pass --delete-state to also delete saved state.")
		}
		return 0
	}

	// Daemon is not running: log out saved state directly if present.
	if remote.TailscaleStateExists(dir) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := remoteLogoutDir(ctx, remote.TailscaleDir(dir), s.Remote.Tailscale.Hostname)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "pitwall: tailscale logout: %v\n", err)
		}
		if *deleteState {
			if err := remote.RemoveTailscaleState(dir); err != nil {
				fmt.Fprintln(stderr, "pitwall:", err)
				return 1
			}
		}
	} else if *deleteState {
		_ = remote.RemoveTailscaleState(dir)
	}
	_ = remote.WriteTailscaleStatus(dir, remote.TailscaleStatus{
		Enabled:  false,
		State:    remote.TailscaleOff,
		Hostname: s.Remote.Tailscale.Hostname,
	})
	if *deleteState {
		fmt.Fprintln(stdout, "Built-in Tailscale is off and its saved state was deleted.")
	} else {
		fmt.Fprintln(stdout, "Built-in Tailscale is off.")
	}
	return 0
}

type tailscaleStatusJSON struct {
	Enabled       bool     `json:"enabled"`
	State         string   `json:"state"`
	DaemonRunning bool     `json:"daemon_running"`
	Hostname      string   `json:"hostname"`
	DNSName       string   `json:"dns_name,omitempty"`
	URL           string   `json:"url,omitempty"`
	AuthURL       string   `json:"auth_url,omitempty"`
	SelfUser      string   `json:"self_user,omitempty"`
	IPs           []string `json:"ips,omitempty"`
	AllowUsers    []string `json:"allow_users,omitempty"`
	AllowTags     []string `json:"allow_tags,omitempty"`
	StateDir      string   `json:"state_dir"`
	StateExists   bool     `json:"state_exists"`
	Error         string   `json:"error,omitempty"`
}

func runRemoteTailscaleStatus(args []string, dir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote tailscale status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(stderr, remoteTailscaleUsage)
		return 2
	}
	s, _ := config.Load()
	tsCfg := s.Remote.Tailscale
	daemonUp := remoteDaemonRunning()
	st, ok := remote.ReadTailscaleStatus(dir)
	state := remote.TailscaleOff
	if ok && daemonUp && st.State != "" {
		state = st.State
	}
	hostname := tsCfg.Hostname
	if ok && daemonUp && st.Hostname != "" {
		hostname = st.Hostname
	}
	out := tailscaleStatusJSON{
		Enabled:       tsCfg.Enabled,
		State:         state,
		DaemonRunning: daemonUp,
		Hostname:      hostname,
		AllowUsers:    tsCfg.AllowUsers,
		AllowTags:     tsCfg.AllowTags,
		StateDir:      remote.TailscaleDir(dir),
		StateExists:   remote.TailscaleStateExists(dir),
	}
	if ok && daemonUp {
		out.DNSName = st.DNSName
		out.URL = st.URL
		out.AuthURL = st.AuthURL
		out.SelfUser = st.SelfUser
		out.IPs = st.IPs
		out.Error = st.Error
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	fmt.Fprintf(stdout, "Enabled: %v\n", out.Enabled)
	fmt.Fprintf(stdout, "State:   %s", out.State)
	if !daemonUp {
		fmt.Fprint(stdout, " (daemon not running)")
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Device:  %s\n", out.Hostname)
	if out.URL != "" {
		fmt.Fprintf(stdout, "URL:     %s\n", out.URL)
	}
	if out.AuthURL != "" {
		fmt.Fprintf(stdout, "Login:   %s\n", out.AuthURL)
	}
	if len(out.IPs) > 0 {
		fmt.Fprintf(stdout, "IPs:     %s\n", strings.Join(out.IPs, ", "))
	}
	if out.Error != "" {
		fmt.Fprintf(stdout, "Error:   %s\n", out.Error)
	}
	fmt.Fprintf(stdout, "Access:  %s\n", formatTailscaleAccess(out.SelfUser, out.AllowUsers, out.AllowTags))
	fmt.Fprintf(stdout, "State dir: %s (saved: %v)\n", out.StateDir, out.StateExists)
	return 0
}

func formatTailscaleAccess(selfUser string, allowUsers, allowTags []string) string {
	var parts []string
	if selfUser != "" {
		parts = append(parts, selfUser+" (owner)")
	} else {
		parts = append(parts, "owner user after login")
	}
	parts = append(parts, allowUsers...)
	parts = append(parts, allowTags...)
	return strings.Join(parts, ", ")
}
