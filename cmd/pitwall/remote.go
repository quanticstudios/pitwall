package main

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/qr"
	"github.com/quanticstudios/pitwall/internal/remote"
)

const remoteUsage = `usage:
  pitwall remote pair [name]     show a QR code that pairs a phone, good for 10 minutes
  pitwall remote devices         list paired devices
  pitwall remote revoke <name>   unpair a device, by name or id
Turn the phone page on with [remote] enabled = true in config.toml; see
docs/phone.md for tailscale serve.
`

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
		fmt.Fprintf(stdout, "\nScan this with your phone, or open:\n  %s\n\nIt pairs %q once, within %.0f minutes.\n", url, remote.PairName(dir, time.Now()), remote.PairTTL.Minutes())
		if s.Remote.TLS {
			if _, fp, err := remote.Cert(dir); err == nil {
				fmt.Fprintf(stdout, "The page's certificate is pitwall's own. Before you trust it, check its SHA-256 is\n  %s\n", fp)
			}
		}
		if host, port, _ := net.SplitHostPort(s.Remote.Listen); s.Remote.URL == "" && config.Loopback(host) {
			fmt.Fprintf(stdout, "\nThat address is this computer's own. Put the page on your tailnet with\n  tailscale serve --bg %s\nand set url under [remote] to the https address it prints, then pair again.\n", port)
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
	default:
		fmt.Fprint(stderr, remoteUsage)
		return 2
	}
	return 0
}
