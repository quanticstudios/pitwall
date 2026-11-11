package mobile

import (
	"context"
	"errors"
	"image"
	"image/color"
	"net/url"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/styledtext"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pairView is the first screen, in three steps: scan the QR code pitwall
// remote pair shows, paste the link it prints, or type the address and
// the code, then, for a server with pitwall's own certificate, compare
// its SHA-256. Nothing is saved until pairing works.
type pairView struct {
	link, addr, code               widget.Editor
	paste, go_, swap, back, cancel widget.Clickable
	scan, cancelScan               widget.Clickable
	// manual shows the address and code in place of the link.
	manual bool
	// scanning shows the camera; shown is the frame img holds.
	scanning bool
	shown    *frame
	img      paint.ImageOp
	busy     bool
	note     string // the last attempt's error, shown under its field
	// untrusted is the certificate pitwall made for itself, which the
	// user can trust once its SHA-256 matches what pair printed; server
	// and code are the attempt it answered.
	untrusted, server, pairCode string
	list                        layout.List
}

func (p *pairView) init() {
	for _, ed := range []*widget.Editor{&p.link, &p.addr, &p.code} {
		ed.SingleLine, ed.Submit = true, true
	}
	p.link.InputHint, p.addr.InputHint, p.code.InputHint = key.HintURL, key.HintURL, key.HintText
	p.list.Axis = layout.Vertical
}

func (a *App) pairLayout(gtx C) {
	p, th := &a.pair, a.th
	if p.scanning && a.scanEvents(gtx) {
		return
	}
	if p.scan.Clicked(gtx) && !p.busy {
		a.startScan()
	}
	submit := false
	for _, ed := range []*widget.Editor{&p.link, &p.addr, &p.code} {
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			switch ev.(type) {
			case widget.SubmitEvent:
				submit = true
			case widget.ChangeEvent:
				p.note = ""
			}
		}
	}
	if p.paste.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: &p.link})
		gtx.Execute(clipboard.ReadCmd{Tag: &p.link})
	}
	if p.swap.Clicked(gtx) {
		p.manual, p.note = !p.manual, ""
	}
	if p.back.Clicked(gtx) {
		p.untrusted, p.note = "", ""
	}
	if p.cancel.Clicked(gtx) {
		a.adding, p.note, p.untrusted, p.manual = false, "", "", false
		return
	}
	if !p.scanning && (p.untrusted != "" || p.manual || a.adding) {
		// Back steps back: the certificate to the step before, the
		// address and code to the link, the link to the sidebar while
		// adding a machine. On a first pairing's link it goes unread, so
		// Android leaves the app.
		for {
			ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
			if !ok {
				break
			}
			if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
				switch {
				case p.untrusted != "":
					p.untrusted = ""
				case p.manual:
					p.manual = false
				default:
					a.adding = false
				}
				p.note = ""
			}
		}
	}
	if (p.go_.Clicked(gtx) || submit) && !p.busy {
		if p.untrusted != "" {
			a.startPair(p.server, p.pairCode, p.untrusted)
		} else {
			link, code := p.link.Text(), ""
			if p.manual {
				link, code = p.addr.Text(), p.code.Text()
			}
			if server, c, err := phone.ParseInvite(link, code); err != nil {
				p.note = sentence(err.Error())
			} else {
				a.startPair(server, c, "")
			}
		}
	}

	fillMax(gtx, th.Bg)
	var w []layout.Widget
	switch {
	case p.scanning:
		w = a.scanStep()
	case p.untrusted != "":
		w = a.certStep()
	case p.manual:
		w = a.codeStep()
	default:
		w = a.linkStep()
	}
	in := layout.Inset{Top: 44, Left: pad + 2, Right: pad + 2, Bottom: pad}
	in.Layout(gtx, func(gtx C) D {
		return p.list.Layout(gtx, len(w), func(gtx C, i int) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w[i](gtx)
		})
	})
}

// pairHead is a step's mark, title and words under it, where every odd
// part of words is code.
func (a *App) pairHead(title string, words ...string) []layout.Widget {
	th := a.th
	return []layout.Widget{
		func(gtx C) D { return sidebar.Logo(gtx, th, gtx.Dp(24)) },
		gap(16),
		func(gtx C) D { return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Title)*1.15, th.Fg, 0, title) },
		gap(6),
		func(gtx C) D { return prose(gtx, th, th.Muted, words...) },
		gap(18),
	}
}

// prose draws parts as one paragraph in small text, every odd part in
// the mono font, as inline code.
func prose(gtx C, th *theme.Theme, col color.NRGBA, parts ...string) D {
	spans := make([]styledtext.SpanStyle, 0, len(parts))
	for i, s := range parts {
		sp := styledtext.SpanStyle{Font: th.UIFont, Size: th.Sp(theme.Small), Color: col, Content: s}
		if i%2 == 1 {
			sp.Font, sp.Color = th.MonoFont, th.Fg
		}
		spans = append(spans, sp)
	}
	return styledtext.Text(th.Shaper, spans...).Layout(gtx, nil)
}

func (a *App) linkStep() []layout.Widget {
	p, th := &a.pair, a.th
	title := "Pair with your computer"
	if a.adding {
		title = "Add a machine"
	}
	w := a.pairHead(title, "On the computer, run ", "pitwall remote pair", " and paste the link it prints.")
	if canScan {
		w = a.pairHead(title, "On the computer, run ", "pitwall remote pair", ", then scan the QR code it shows or paste the link it prints.")
		w = append(w, func(gtx C) D {
			return kit.WideButton(gtx, th, &p.scan, kit.Secondary.When(!p.busy), kit.Large, "Scan QR code")
		}, gap(18))
	}
	w = append(w,
		fieldLabel(th, "Pairing link"),
		func(gtx C) D {
			return pairField(gtx, th, &p.link, "https://box.example.com/#pair=…", th.MonoFont, p.note != "", &p.paste)
		},
	)
	w = append(w, a.pairNote()...)
	w = append(w, gap(14), a.pairButton("Pair"), gap(6), a.swapButton("Enter the address and code instead"))
	if a.adding {
		w = append(w, func(gtx C) D {
			return layout.Center.Layout(gtx, func(gtx C) D { return textButton(gtx, th, &p.cancel, "Cancel") })
		})
	}
	return w
}

func (a *App) codeStep() []layout.Widget {
	p, th := &a.pair, a.th
	w := a.pairHead("Pair with your computer", "The address is pitwall's ", "[remote] url", "; a code is good for 10 minutes.")
	w = append(w,
		fieldLabel(th, "Address"),
		func(gtx C) D { return pairField(gtx, th, &p.addr, "https://box.example.com", th.MonoFont, false, nil) },
		gap(14),
		fieldLabel(th, "Code"),
		func(gtx C) D { return pairField(gtx, th, &p.code, "ABCD2345…", th.MonoFont, p.note != "", nil) },
	)
	w = append(w, a.pairNote()...)
	return append(w, gap(14), a.pairButton("Pair"), gap(6), a.swapButton("Paste a link instead"))
}

func (a *App) certStep() []layout.Widget {
	p, th := &a.pair, a.th
	host := p.server
	if u, err := url.Parse(p.server); err == nil && u.Host != "" {
		host = u.Host
	}
	w := a.pairHead("Check the certificate", "pitwall made its own certificate for ", host, ". Compare its SHA-256 with the one ", "pitwall remote pair", " printed.")
	w = append(w,
		fieldLabel(th, "SHA-256"),
		func(gtx C) D {
			return box(gtx, th.Surface, th.BorderStrong, theme.RadiusControl, layout.UniformInset(10), func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return label(gtx, th, th.MonoFont, th.Sp(theme.Body), th.Fg, 0, fingerprintLines(p.untrusted))
			})
		},
	)
	w = append(w, a.pairNote()...)
	return append(w, gap(14), a.pairButton("They match, pair"), gap(6), func(gtx C) D {
		return layout.Center.Layout(gtx, func(gtx C) D { return textButton(gtx, th, &p.back, "Cancel") })
	})
}

// fingerprintLines breaks a SHA-256 into four lines of eight bytes, so
// the eye can compare it a line at a time.
func fingerprintLines(fp string) string {
	parts := strings.Split(fp, ":")
	var lines []string
	for len(parts) > 8 {
		lines, parts = append(lines, strings.Join(parts[:8], ":")), parts[8:]
	}
	return strings.Join(append(lines, strings.Join(parts, ":")), "\n")
}

func fieldLabel(th *theme.Theme, s string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D { return small(gtx, th, th.Muted, s) })
	}
}

func (a *App) pairNote() []layout.Widget {
	p, th := &a.pair, a.th
	if p.note == "" {
		return nil
	}
	return []layout.Widget{gap(6), func(gtx C) D { return small(gtx, th, th.Readable(th.Red, th.Bg), p.note) }}
}

func (a *App) pairButton(s string) layout.Widget {
	p, th := &a.pair, a.th
	if p.busy {
		s = "Pairing…"
	}
	return func(gtx C) D {
		return kit.WideButton(gtx, th, &p.go_, kit.Primary.When(!p.busy), kit.Large, s)
	}
}

func (a *App) swapButton(s string) layout.Widget {
	return func(gtx C) D {
		return layout.Center.Layout(gtx, func(gtx C) D { return textButton(gtx, a.th, &a.pair.swap, s) })
	}
}

// pairField is a ringed single-line field with hint while it is empty:
// primary while focused, red with bad, and with paste, a clipboard button
// at its end. A tap anywhere in it brings up the soft keyboard.
func pairField(gtx C, th *theme.Theme, ed *widget.Editor, hint string, f font.Font, bad bool, paste *widget.Clickable) D {
	ring := th.BorderStrong
	switch {
	case bad:
		ring = th.Red
	case gtx.Focused(ed):
		ring = theme.Mix(th.Bg, th.Primary, 0.7)
	}
	fs := fields[ed]
	if fs == nil {
		fs = new(widget.Clickable)
		fields[ed] = fs
	}
	if fs.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: ed})
		gtx.Execute(key.SoftKeyboardCmd{Show: true})
	}
	h := gtx.Dp(touch + 4)
	return fs.Layout(gtx, func(gtx C) D {
		size := image.Pt(gtx.Constraints.Max.X, h)
		rr := gtx.Dp(theme.RadiusControl)
		paint.FillShape(gtx.Ops, ring, clip.UniformRRect(image.Rectangle{Max: size}, rr).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rectangle{Max: size}.Inset(1), rr-1).Op(gtx.Ops))
		gtx.Constraints = layout.Exact(size)
		return layout.Inset{Left: 10, Right: 2}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Min.Y = 0
					return layout.W.Layout(gtx, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						size := th.Sp(theme.Body)
						return layout.Stack{}.Layout(gtx,
							layout.Stacked(func(gtx C) D {
								if ed.Len() > 0 {
									return D{}
								}
								return label(gtx, th, f, size, th.TextQuiet, 1, hint)
							}),
							layout.Stacked(func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return ed.Layout(gtx, th.Shaper, f, size, colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.Bg, th.Primary, 0.35)))
							}),
						)
					})
				}),
				layout.Rigid(func(gtx C) D {
					if paste == nil {
						return D{Size: image.Pt(gtx.Dp(8), 0)}
					}
					gtx.Constraints.Min.Y = 0
					return iconButton(gtx, th, paste, icon("clipboard"))
				}),
			)
		})
	})
}

func gap(dp unit.Dp) layout.Widget {
	return func(gtx C) D { return D{Size: image.Pt(0, gtx.Dp(dp))} }
}

// startPair trades code for a device token at server, trusting the
// certificate pin when set, and opens the sidebar once it works.
func (a *App) startPair(server, code, pin string) {
	p := &a.pair
	p.busy, p.note = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := phone.Pair(ctx, server, code, pin)
		a.post(func() {
			p.busy = false
			if err == nil {
				err = a.addMachine(c)
			}
			var ue *phone.UntrustedError
			switch {
			case errors.As(err, &ue):
				p.untrusted, p.server, p.pairCode = ue.Fingerprint, server, code
			case err != nil:
				p.note = sentence(err.Error())
			default:
				*p = pairView{}
				p.init()
			}
		})
	}()
}
