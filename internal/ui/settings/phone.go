package settings

import (
	"fmt"
	"image"
	"image/color"
	"net"
	"strings"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/qr"
	"github.com/quanticstudios/pitwall/internal/remote"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// phonePage is the Phone category's state.
type phonePage struct {
	dir     string // remote.Dir; tests set it
	devices []remote.Device
	code    qr.Code // the pairing being shown, nil for none
	url     string
	name    string // the device it pairs
	until   time.Time
	fp      string // the certificate's SHA-256, with tls
	note    string // what happened last: paired, revoked, or an error
	test    pushTest
}

func (p *Page) phoneDir() string {
	if p.ph.dir == "" {
		p.ph.dir = remote.Dir()
	}
	return p.ph.dir
}

// readDevices refreshes the paired devices.
func (p *Page) readDevices() {
	devs, err := remote.Devices(p.phoneDir())
	p.ph.devices = devs
	if err != nil {
		p.ph.note = err.Error()
	}
}

// pairPhone starts a pairing and shows its QR code.
func (p *Page) pairPhone() {
	code, err := remote.Pair(p.phoneDir(), "", time.Now())
	if err == nil {
		p.ph.url = remote.PairURL(p.s.Remote, code)
		p.ph.code, err = qr.Encode(p.ph.url)
	}
	if err != nil {
		p.ph.code, p.ph.note = nil, err.Error()
		return
	}
	p.ph.name, p.ph.until, p.ph.note, p.ph.fp = remote.PairName(p.phoneDir(), time.Now()), time.Now().Add(remote.PairTTL), "", ""
	if p.s.Remote.TLS {
		if _, fp, err := remote.Cert(p.phoneDir()); err == nil {
			p.ph.fp = fp
		}
	}
}

// pairing checks the code shown: gone once used or expired.
func (p *Page) pairing(now time.Time) {
	if p.ph.code == nil || remote.PairName(p.phoneDir(), now) != "" {
		return
	}
	p.ph.code = nil
	before := len(p.ph.devices)
	p.readDevices()
	if len(p.ph.devices) > before {
		p.ph.note = "Paired " + p.ph.name + "."
	}
}

func (p *Page) phone() []section {
	th := p.th
	r := p.s.Remote
	host, port, _ := net.SplitHostPort(r.Listen)
	where := "on " + r.Listen + ", which only this computer reaches. Put it on your tailnet with tailscale serve --bg " + port + " and set url under [remote] to the address it prints, or use an SSH tunnel."
	if !config.Loopback(host) {
		where = "on " + r.Listen + " over HTTPS, with a certificate pitwall makes; phones on your network can reach it."
	}
	if r.URL != "" {
		where += " Phones open " + r.URL + "."
	}
	rows := []row{{label: "Answer from your phone",
		desc: "Serves a page where a paired phone sees which agents need you, with Allow and Deny for permission prompts and a reply box for questions. " +
			"It shows what the sidebar shows, never a pane's screen, and has no account or relay. It runs " + where,
		extra:   "phone remote mobile pair tailscale approve qr",
		control: p.toggle("remote", "enabled", r.Enabled)}}
	if !r.Enabled {
		return []section{{rows: rows}}
	}
	btn := func(id, label string, kind btnKind, click func()) gl.Widget {
		return func(gtx gl.Context) gl.Dimensions {
			c := p.btn(id)
			for c.Clicked(gtx) {
				click()
				gtx.Execute(op.InvalidateCmd{}) // the rows of this frame are already built
			}
			return p.button(gtx, c, kind, label)
		}
	}
	note := func(gtx gl.Context) gl.Dimensions {
		if p.ph.note == "" {
			return gl.Dimensions{}
		}
		return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, p.ph.note)
	}
	pair := row{label: "Pair a phone", desc: "Shows a QR code that pairs one phone. The phone keeps a token; this computer keeps only its hash.",
		extra: "pair qr code phone device", control: btn("rpair", "Pair", primary, p.pairPhone), below: note}
	if p.ph.code != nil {
		_, code, _ := strings.Cut(p.ph.url, "#pair=")
		pair.label, pair.desc = "Scan with your phone", fmt.Sprintf("Or open %s, or enter %s on the page. It pairs %q once, until %s.", p.ph.url, code, p.ph.name, p.ph.until.Format("15:04"))
		if p.ph.fp != "" {
			pair.desc += " The page's certificate SHA-256 is " + p.ph.fp + "."
		}
		pair.control = btn("rpair", "New code", secondary, p.pairPhone)
		pair.wide = true
		pair.below = func(gtx gl.Context) gl.Dimensions {
			now := gtx.Now
			p.pairing(now)
			gtx.Execute(op.InvalidateCmd{At: now.Add(time.Second)})
			if p.ph.code == nil {
				return note(gtx)
			}
			return gl.Inset{Top: 12}.Layout(gtx, func(gtx gl.Context) gl.Dimensions { return drawQR(gtx, p.ph.code) })
		}
	}
	rows = append(rows, pair)
	var devs []row
	for _, d := range p.ph.devices {
		devs = append(devs, row{label: d.Name, desc: "Paired " + d.Paired.Local().Format("2 Jan 2006, 15:04") + ". Its id is " + d.ID + ".",
			extra: "device revoke unpair phone",
			control: btn("rrevoke:"+d.ID, "Revoke", danger, func() {
				if _, err := remote.Revoke(p.phoneDir(), d.ID); err != nil {
					p.ph.note = err.Error()
				} else {
					p.ph.note = "Revoked " + d.Name + "."
				}
				p.readDevices()
			})}, p.pushRow(d, btn))
	}
	secs := []section{{rows: rows}}
	if len(devs) > 0 {
		secs = append(secs, section{title: "Paired devices", desc: "Revoking one stops its token at once.", rows: devs})
	}
	return secs
}

// drawQR draws c dark on light with a quiet zone, about 200dp across.
func drawQR(gtx gl.Context, c qr.Code) gl.Dimensions {
	const quiet = 4
	n := len(c) + 2*quiet
	m := max(gtx.Dp(200)/n, 2)
	sz := image.Pt(n*m, n*m)
	rrect(gtx, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, image.Rectangle{Max: sz}, gtx.Dp(6))
	for y, row := range c {
		for x, dark := range row {
			if dark {
				at := image.Pt((x+quiet)*m, (y+quiet)*m)
				rrect(gtx, color.NRGBA{A: 255}, image.Rectangle{Min: at, Max: at.Add(image.Pt(m, m))}, 0)
			}
		}
	}
	return gl.Dimensions{Size: sz}
}
