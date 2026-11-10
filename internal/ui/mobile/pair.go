package mobile

import (
	"context"
	"errors"
	"image"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pairView is the first screen: the link pitwall remote pair prints, or
// the address and the code, traded for a device token.
type pairView struct {
	link, code        widget.Editor
	paste, go_, trust widget.Clickable
	busy              bool
	note              string
	bad               bool
	// untrusted is the certificate pitwall made for itself, which the
	// user can trust once its SHA-256 matches what pair printed.
	untrusted string
	list      layout.List
}

func (p *pairView) init() {
	p.link.SingleLine, p.link.Submit = true, true
	p.code.SingleLine, p.code.Submit = true, true
	p.link.InputHint, p.code.InputHint = key.HintURL, key.HintText
	p.list.Axis = layout.Vertical
}

func (a *App) pairLayout(gtx C) {
	p, th := &a.pair, a.th
	submit := false
	for _, ed := range []*widget.Editor{&p.link, &p.code} {
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				submit = true
			}
			if _, ok := ev.(widget.ChangeEvent); ok && p.untrusted != "" {
				p.untrusted, p.note = "", ""
			}
		}
	}
	if p.paste.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: &p.link})
		gtx.Execute(clipboard.ReadCmd{Tag: &p.link})
	}
	pin := ""
	if p.trust.Clicked(gtx) && p.untrusted != "" {
		submit, pin = true, p.untrusted
	}
	if (p.go_.Clicked(gtx) || submit) && !p.busy {
		a.startPair(pin)
	}

	fillMax(gtx, th.Sidebar)
	in := layout.Inset{Top: 32, Left: pad + 4, Right: pad + 4, Bottom: pad}
	w := []layout.Widget{
		func(gtx C) D {
			return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Title)*1.4, th.Fg, 1, "pitwall")
		},
		gap(6),
		func(gtx C) D {
			return body(gtx, th, th.Muted, "Pair this phone with the computer that runs your agents. On it, run pitwall remote pair, or press Pair in Settings, Phone, and paste the link it shows.")
		},
		gap(20),
		func(gtx C) D { return small(gtx, th, th.Muted, "Link or address") },
		gap(6),
		func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return field(gtx, th, &p.link, "https://box.example.com/#pair=…", th.UIFont)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return button(gtx, th, &p.paste, kit.Secondary, "Paste") })
				}),
			)
		},
		gap(14),
		func(gtx C) D { return small(gtx, th, th.Muted, "Code, if the link has none") },
		gap(6),
		func(gtx C) D { return field(gtx, th, &p.code, "ABCD2345…", th.MonoFont) },
		gap(20),
	}
	if p.untrusted != "" {
		w = append(w,
			func(gtx C) D {
				return body(gtx, th, th.Fg, "This address uses pitwall's own certificate. Trust it only if its SHA-256 matches the one pitwall remote pair printed:")
			},
			gap(8),
			func(gtx C) D { return label(gtx, th, th.MonoFont, th.Sp(theme.Small), th.Fg, 0, p.untrusted) },
			gap(14),
			func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return button(gtx, th, &p.trust, kit.Primary.When(!p.busy), "Trust and pair") }),
				)
			},
		)
	} else {
		w = append(w, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return button(gtx, th, &p.go_, kit.Primary.When(!p.busy), map[bool]string{false: "Pair", true: "Pairing…"}[p.busy])
		})
	}
	if p.note != "" {
		col := th.Muted
		if p.bad {
			col = th.Readable(th.Red, th.Sidebar)
		}
		w = append(w, gap(12), func(gtx C) D { return body(gtx, th, col, sentence(p.note)) })
	}
	in.Layout(gtx, func(gtx C) D {
		return p.list.Layout(gtx, len(w), func(gtx C, i int) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w[i](gtx)
		})
	})
}

func gap(dp unit.Dp) layout.Widget {
	return func(gtx C) D { return D{Size: image.Pt(0, gtx.Dp(dp))} }
}

func (a *App) startPair(pin string) {
	p := &a.pair
	server, code, err := phone.ParseInvite(p.link.Text(), p.code.Text())
	if err != nil {
		p.note, p.bad = err.Error(), true
		return
	}
	p.busy, p.note, p.bad = true, "", false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := phone.Pair(ctx, server, code, pin)
		if err == nil {
			err = phone.Save(a.dir, c)
		}
		a.post(func() {
			p.busy = false
			var ue *phone.UntrustedError
			switch {
			case errors.As(err, &ue):
				p.untrusted, p.note, p.bad = ue.Fingerprint, "", false
			case err != nil:
				p.note, p.bad = err.Error(), true
			default:
				p.untrusted, p.note = "", ""
				p.link.SetText("")
				p.code.SetText("")
				a.start(c)
			}
		})
	}()
}
