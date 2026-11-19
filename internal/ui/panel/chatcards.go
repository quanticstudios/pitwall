package panel

import (
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// agent is the chat's agent by name: Claude, Codex.
func (d *cdraw) agent() string {
	if n := sidebar.AgentName(d.s.Provider); n != "" {
		return n
	}
	return "The agent"
}

// requestWhat is what an approval asks to run or touch, in one line, a
// file under dir relative to it.
func requestWhat(a session.Approval, dir string) string {
	r := a.Request
	if r == nil {
		return firstLine(deref(a.Detail))
	}
	switch {
	case r.Command != "":
		return strings.TrimSpace(r.Command)
	case r.FilePath != "":
		return relPath(dir, r.FilePath)
	}
	if arg := flow.ToolArg(session.ToolCall{ToolName: r.ToolName, Input: r.Input}); arg != "" {
		return r.ToolName + " " + arg
	}
	return r.ToolName
}

// approvalWhy is an approval card's muted line under what it runs: the
// request's detail, unless that only restates the title and the command,
// as Claude's "Bash: git push" or a provider's "Edit needs approval".
func approvalWhy(a session.Approval) string {
	why := strings.TrimSpace(deref(a.Detail))
	if why == requestWhat(a, "") || strings.HasSuffix(why, " needs approval") ||
		a.Request != nil && a.Request.ToolName != "" && strings.HasPrefix(why, a.Request.ToolName+": ") {
		return ""
	}
	return why
}

// requestTitle is an approval card's title.
func (d *cdraw) requestTitle(a session.Approval) string {
	switch a.RequestType {
	case session.RequestCommand:
		return d.agent() + " wants to run a command"
	case session.RequestFileChange:
		return d.agent() + " wants to edit a file"
	case session.RequestFileRead:
		return d.agent() + " wants to read a file"
	}
	if a.Request != nil && a.Request.ToolName != "" {
		return d.agent() + " wants to use " + a.Request.ToolName
	}
	return d.agent() + " asks for approval"
}

// tail is what follows the items: the chat's error, the requests it waits
// on, and a working line before a turn has items.
func (d *cdraw) tail() []layout.Widget {
	s := d.s
	var out []layout.Widget
	if n := len(s.Content); s.SessionState == session.StateRunning && (n == 0 || s.Content[n-1].Kind == session.KindUserPrompt) {
		start := parseTime(s.UpdatedAt)
		if n > 0 {
			start = parseTime(s.Content[n-1].UpdatedAt)
		}
		out = append(out, func(gtx layout.Context) layout.Dimensions { return d.working(gtx, start) })
	}
	if s.LastError != nil && *s.LastError != "" {
		msg := *s.LastError
		out = append(out, func(gtx layout.Context) layout.Dimensions {
			return d.selectable(gtx, d.t.sel("error"), d.body(d.th.Readable(d.th.Red, d.th.Bg)), msg)
		})
	}
	for _, a := range s.PendingApprovals {
		if d.t.sent[a.ID] {
			continue
		}
		out = append(out, func(gtx layout.Context) layout.Dimensions { return d.approvalCard(gtx, a) })
	}
	for _, in := range s.PendingUserInputs {
		if d.t.sent[in.ID] || d.t.asks[in.ID] == nil {
			continue
		}
		out = append(out, func(gtx layout.Context) layout.Dimensions { return d.askCard(gtx, in) })
	}
	if p := s.PlanApproval; p != nil {
		out = append(out, func(gtx layout.Context) layout.Dimensions {
			return d.card(gtx, "check", "The plan is ready", func(gtx layout.Context) layout.Dimensions {
				return d.selectable(gtx, d.t.sel("plan:"+p.ID), d.body(d.th.Fg), strings.TrimSpace(deref(p.Detail)))
			})
		})
	}
	return out
}

// working is the header of a turn that has nothing to show yet.
func (d *cdraw) working(gtx layout.Context, start time.Time) layout.Dimensions {
	th := d.th
	d.t.live = true
	hh := gtx.Dp(rowH) + 2
	w := gtx.Constraints.Max.X
	rrect(gtx, image.Rect(0, 0, w, hh), gtx.Dp(theme.RadiusPopover), th.Surface, th.BorderSubtle)
	x := gtx.Dp(rowPad) + gtx.Dp(14) + gtx.Dp(theme.SpaceS)
	at(gtx, image.Pt(x, (hh-gtx.Dp(11))/2), func(gtx layout.Context) layout.Dimensions {
		return spinner(gtx, gtx.Dp(11), th.Surface, th.Blue, d.sec)
	})
	x += gtx.Dp(11) + gtx.Dp(theme.SpaceS)
	title := "Working"
	if !start.IsZero() {
		title = logTitle(d.t.now.Sub(start), 0, true)
	}
	c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, title)
	})
	at(gtx, image.Pt(x, (hh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
	return layout.Dimensions{Size: image.Pt(w, hh)}
}

// card is a request card in the needs-you yellow: an icon and title, then
// body.
func (d *cdraw) card(gtx layout.Context, ic, title string, body layout.Widget) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	yellow := th.Readable(th.Yellow, th.Surface)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*gtx.Dp(cardPadX)), gtx.Constraints.Max.Y)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions {
				return hrow(gtx, gtx.Dp(theme.SpaceS),
					func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, ic, gtx.Dp(14), yellow) },
					func(gtx layout.Context) layout.Dimensions {
						return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), yellow, title)
					})
			},
			body,
		)
	})
	h := dims.Size.Y + 2*gtx.Dp(cardPadY)
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(10), th.Surface, theme.Mix(th.Surface, th.Yellow, 0.3))
	at(gtx, image.Pt(gtx.Dp(cardPadX), gtx.Dp(cardPadY)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return dims })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// approvalCard is a pending approval: what it runs in mono, why it asks
// when the request says, and Allow 1, Allow for session 2, Deny 3.
func (d *cdraw) approvalCard(gtx layout.Context, a session.Approval) layout.Dimensions {
	th := d.th
	bs := d.t.answer[a.ID]
	if bs == nil {
		bs = new([3]widget.Clickable)
		d.t.answer[a.ID] = bs
	}
	what, why := requestWhat(a, d.t.Dir), approvalWhy(a)
	return d.card(gtx, "shield", d.requestTitle(a), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions {
				if what == "" {
					return layout.Dimensions{}
				}
				return d.monoBox(gtx, "req:"+a.ID, what)
			},
			func(gtx layout.Context) layout.Dimensions {
				if why == "" {
					return layout.Dimensions{}
				}
				return d.selectable(gtx, d.t.sel("why:"+a.ID), d.small(th.Muted), why)
			},
			func(gtx layout.Context) layout.Dimensions {
				return hrow(gtx, gtx.Dp(theme.SpaceS),
					func(gtx layout.Context) layout.Dimensions { return d.keyButton(gtx, &bs[0], true, "Allow", "1", true) },
					func(gtx layout.Context) layout.Dimensions {
						return d.keyButton(gtx, &bs[1], false, "Allow for session", "2", true)
					},
					func(gtx layout.Context) layout.Dimensions { return d.keyButton(gtx, &bs[2], false, "Deny", "3", true) },
				)
			},
		)
	})
}

// monoBox is a command in mono on the page's background.
func (d *cdraw) monoBox(gtx layout.Context, key, s string) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel(key), d.mono(), s)
	})
	h := td.Size.Y + 2*py
	paint.FillShape(gtx.Ops, th.Bg, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// askCard is a pending question: each question's options as numbered
// rows, the recommended one picked, a row for a typed answer, Answer.
func (d *cdraw) askCard(gtx layout.Context, in session.InputRequest) layout.Dimensions {
	th := d.th
	a := d.t.asks[in.ID]
	_, ready := a.answers(in.Questions)
	return d.card(gtx, "ask", d.agent()+" has a question", func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var ws []layout.Widget
		for qi, q := range in.Questions {
			if qi >= len(a.opts) {
				break
			}
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return d.selectable(gtx, d.t.sel("q:"+in.ID+"/"+q.ID), tstyle{medium(th.UIFont), th.Sp(theme.Body), th.Fg, 1.45}, q.Prompt)
			})
			opts := func(gtx layout.Context) layout.Dimensions {
				var rows []layout.Widget
				for oi, o := range q.Options {
					rows = append(rows, func(gtx layout.Context) layout.Dimensions {
						return d.option(gtx, &a.opts[qi][oi], a.pick[qi] == oi, strconv.Itoa(oi+1), func(gtx layout.Context) layout.Dimensions {
							return vstack(gtx, gtx.Dp(1),
								func(gtx layout.Context) layout.Dimensions {
									return label(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, o.Label, 0)
								},
								func(gtx layout.Context) layout.Dimensions {
									if o.Description == "" {
										return layout.Dimensions{}
									}
									return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, o.Description, 0)
								})
						})
					})
				}
				n := len(q.Options)
				rows = append(rows, func(gtx layout.Context) layout.Dimensions {
					return d.option(gtx, &a.opts[qi][n], a.pick[qi] == n, strconv.Itoa(n+1), func(gtx layout.Context) layout.Dimensions {
						return d.otherField(gtx, &a.other[qi])
					})
				})
				return vstack(gtx, gtx.Dp(theme.SpaceXS), rows...)
			}
			ws = append(ws, opts)
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return d.keyButton(gtx, &a.submit, true, "Answer", "", ready)
		})
		return vstack(gtx, gtx.Dp(theme.SpaceS), ws...)
	})
}

// option is one of a question's rows: its number on a keycap, then body.
func (d *cdraw) option(gtx layout.Context, c *widget.Clickable, on bool, k string, body layout.Widget) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px-gtx.Dp(32)), gtx.Constraints.Max.Y)}
		call, bd := measure(g, body)
		h := max(bd.Size.Y, gtx.Dp(18), gtx.Dp(hit)-2*py) + 2*py
		fill, ring := th.Surface, th.BorderStrong
		switch {
		case on:
			fill, ring = th.SelectedBg, theme.Mix(th.SelectedBg, th.Primary, 0.45)
		case c.Pressed():
			fill = th.Pressed
		case c.Hovered():
			fill = th.Hover
		}
		rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), fill, ring)
		at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { return d.keycap(gtx, k, false) })
		at(gtx, image.Pt(px+gtx.Dp(32), py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return bd })
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// otherField is a question's free-text row.
func (d *cdraw) otherField(gtx layout.Context, e *widget.Editor) layout.Dimensions {
	th := d.th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	dims := e.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), material(gtx, th.Fg), material(gtx, d.selColor()))
	if e.Len() == 0 {
		label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, "Other: type an answer", 1)
	}
	return dims
}

// resolvedLine is a request answered here: a check, the decision, what it
// was for and when.
func (d *cdraw) resolvedLine(gtx layout.Context, r *resolved) layout.Dimensions {
	th := d.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(40)
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(10), th.Bg, th.BorderStrong)
	x := gtx.Dp(cardPadX)
	icon(gtx, image.Pt(x, (h-gtx.Dp(14))/2), "check", 14, th.Muted)
	x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
	tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, r.when.Local().Format("15:04"))
	})
	lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, r.line)
	})
	base := centerBase(h, ld)
	rx := w - gtx.Dp(cardPadX) - td.Size.X
	at(gtx, image.Pt(rx, onBase(base, td)), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	at(gtx, image.Pt(x, onBase(base, ld)), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
	x += ld.Size.X + gtx.Dp(theme.SpaceS)
	if room := rx - gtx.Dp(12) - x; room > 0 && r.what != "" {
		g := gtx
		g.Constraints.Max.X = room
		f := th.MonoFont
		if r.line == "Answered" {
			f = th.UIFont
		}
		wc, wd := measure(g, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, f, th.Sp(theme.Small), th.TextQuiet, firstLine(r.what))
		})
		at(gtx, image.Pt(x, onBase(base, wd)), func(gtx layout.Context) layout.Dimensions { wc.Add(gtx.Ops); return wd })
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// moreMenu is the quiet ⋯ button at the top right and its menu, which
// holds Open in aide.
func (d *cdraw) moreMenu(gtx layout.Context, size image.Point) {
	th := d.th
	bs := gtx.Dp(hit)
	p := image.Pt(size.X-gtx.Dp(12)-bs, gtx.Dp(10))
	at(gtx, p, func(gtx layout.Context) layout.Dimensions {
		return clickable(gtx, &d.t.more, func(gtx layout.Context) layout.Dimensions {
			if d.t.more.Hovered() || d.t.menu {
				paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, bs, bs), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
			}
			dot := gtx.Dp(3)
			for i := -1; i <= 1; i++ {
				cx := bs/2 + i*gtx.Dp(5)
				circle(gtx, image.Pt(cx-dot/2, bs/2-dot/2), dot, th.Muted)
			}
			return layout.Dimensions{Size: image.Pt(bs, bs)}
		})
	})
	if !d.t.menu {
		return
	}
	mw, ih := gtx.Dp(168), gtx.Dp(hit)
	r := image.Rect(p.X+bs-mw, p.Y+bs+gtx.Dp(4), p.X+bs, p.Y+bs+gtx.Dp(4)+ih+gtx.Dp(8))
	kit.Surface(gtx, r, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	at(gtx, r.Min.Add(image.Pt(gtx.Dp(4), gtx.Dp(4))), func(gtx layout.Context) layout.Dimensions {
		return clickable(gtx, &d.t.aideBtn, func(gtx layout.Context) layout.Dimensions {
			iw := mw - gtx.Dp(8)
			if d.t.aideBtn.Hovered() {
				paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceElevated, th.Fg, 0.06), clip.UniformRRect(image.Rect(0, 0, iw, ih), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
			}
			c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), th.Fg, "Open in aide")
			})
			at(gtx, image.Pt(gtx.Dp(8), (ih-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
			return layout.Dimensions{Size: image.Pt(iw, ih)}
		})
	})
	// The menu takes presses so they do not reach the transcript under it.
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	pointer.CursorDefault.Add(gtx.Ops)
}
