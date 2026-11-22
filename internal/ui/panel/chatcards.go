package panel

import (
	"cmp"
	"image"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
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
		a.Request != nil && a.Request.ToolName != "" && (strings.HasPrefix(why, a.Request.ToolName+": ") || strings.HasPrefix(why, "Approve "+a.Request.ToolName+": ")) {
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
	// A stopped turn already ends on its Interrupted line.
	if msg, stopped := endLine(s); msg != "" && !stopped {
		out = append(out, func(gtx layout.Context) layout.Dimensions { return d.notice(gtx, "error", d.th.Red, "", msg) })
	}
	if l := s.UsageLimit; l != nil {
		out = append(out, func(gtx layout.Context) layout.Dimensions { return d.limitCard(gtx, l) })
	}
	for _, a := range s.PendingApprovals {
		if d.t.sent[a.ID] || a.RequestType == session.RequestPlan && s.PlanApproval != nil && s.PlanApproval.ID == a.ID {
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
	if p := s.PlanApproval; p != nil && !d.t.sent[p.ID] {
		out = append(out, func(gtx layout.Context) layout.Dimensions { return d.planCard(gtx, p) })
	}
	return out
}

// planCard is a plan waiting for approval: the plan as markdown, then
// Approve 1 and Refine 2, which opens a field for what to change and
// sends it back for Claude to plan more.
func (d *cdraw) planCard(gtx layout.Context, p *session.PlanApproval) layout.Dimensions {
	bs := d.t.answer[p.ID]
	if bs == nil {
		bs = new([3]widget.Clickable)
		d.t.answer[p.ID] = bs
	}
	plan := session.Item{ID: "plan:" + p.ID, Text: strings.TrimSpace(deref(p.Detail))}
	e := d.t.reasonField(p.ID)
	return d.card(gtx, "map", "Plan ready for review", func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return vstack(gtx, gtx.Dp(theme.SpaceM),
			func(gtx layout.Context) layout.Dimensions {
				if plan.Text == "" {
					return layout.Dimensions{}
				}
				return d.panelBox(gtx, func(gtx layout.Context) layout.Dimensions { return d.markdown(gtx, plan.ID, d.t.reply(&plan, false)) })
			},
			func(gtx layout.Context) layout.Dimensions {
				return hrow(gtx, gtx.Dp(theme.SpaceS),
					func(gtx layout.Context) layout.Dimensions {
						return d.keyButton(gtx, &bs[0], true, "Approve", "1", true)
					},
					func(gtx layout.Context) layout.Dimensions {
						if e != nil {
							return layout.Dimensions{}
						}
						return d.keyButton(gtx, &bs[2], false, "Refine", "2", true)
					},
				)
			},
			func(gtx layout.Context) layout.Dimensions {
				if e == nil {
					return layout.Dimensions{}
				}
				return d.reasonRow(gtx, e, &bs[2], "Tell "+d.agent()+" what to change", "Refine")
			},
		)
	})
}

// limitNotice is a plan limit's card: which limit, and when it resets.
func limitNotice(l *session.UsageLimit, now time.Time) (title, msg string) {
	title = l.Label + " reached"
	msg = "Claude stopped until the limit resets."
	if l.ResetsAt != nil {
		if at := parseTime(*l.ResetsAt); !at.IsZero() {
			format := "15:04"
			if at.Sub(now) > 20*time.Hour {
				format = "Mon 15:04"
			}
			msg = "Resets " + at.Local().Format(format) + "."
		}
	}
	return title, msg
}

// endLine is the line under a chat whose last turn did not complete: its
// error in red, or a muted Stopped when someone stopped the turn, which
// the snapshot also files as an error.
func endLine(s *session.Snapshot) (msg string, stopped bool) {
	if s.LastError == nil || *s.LastError == "" {
		return "", false
	}
	if n := len(s.Turns); n > 0 && s.Turns[n-1].State == session.TurnInterrupted && *s.LastError == "Turn "+session.TurnInterrupted {
		return "Stopped", true
	}
	return *s.LastError, false
}

// working is the header of a turn that has nothing to show yet.
func (d *cdraw) working(gtx layout.Context, start time.Time) layout.Dimensions {
	if d.ledger {
		return d.ledgerWorking(gtx, start)
	}
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
// when the request says, and Allow 1, Allow for session 2, Deny 3, which
// opens a field for the reason.
func (d *cdraw) approvalCard(gtx layout.Context, a session.Approval) layout.Dimensions {
	if d.ledger {
		return d.ledgerApprovalCard(gtx, a)
	}
	th := d.th
	bs := d.t.answer[a.ID]
	if bs == nil {
		bs = new([3]widget.Clickable)
		d.t.answer[a.ID] = bs
	}
	what, why := requestWhat(a, d.t.Dir), approvalWhy(a)
	e := d.t.reasonField(a.ID)
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
				return wrapRow(gtx, gtx.Dp(theme.SpaceS),
					func(gtx layout.Context) layout.Dimensions { return d.keyButton(gtx, &bs[0], true, "Allow", "1", true) },
					func(gtx layout.Context) layout.Dimensions {
						if a.NoSession { // the prompt has no such answer
							return layout.Dimensions{}
						}
						return d.keyButton(gtx, &bs[1], false, "Allow for session", "2", true)
					},
					func(gtx layout.Context) layout.Dimensions {
						if e != nil {
							return layout.Dimensions{}
						}
						return d.keyButton(gtx, &bs[2], false, "Deny", denyKey(a), true)
					},
				)
			},
			func(gtx layout.Context) layout.Dimensions {
				if e == nil {
					return layout.Dimensions{}
				}
				return d.reasonRow(gtx, e, &bs[2], "Tell "+d.agent()+" what to do instead", "Deny")
			},
		)
	})
}

// denyKey is the key that denies a's card: 3, or 2 when it has no Allow
// for session.
func denyKey(a session.Approval) string {
	if a.NoSession {
		return "2"
	}
	return "3"
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

// askCard is a pending question: one question at a time with a step
// header of clickable pill chips when there are several questions (or a
// mirrored final Review/Submit step), numbered option rows, checkboxes
// for multi-select, an option preview beside them when options carry
// one, a typed Other row, and Back / Next or Submit buttons. A
// one-question single-select card stays compact with no step bar.
func (d *cdraw) askCard(gtx layout.Context, in session.InputRequest) layout.Dimensions {
	th := d.th
	a := d.t.asks[in.ID]
	qs := in.Questions
	if len(qs) == 0 {
		return layout.Dimensions{}
	}
	step := min(max(a.step, 0), max(0, a.numSteps()-1))
	multiStep := len(qs) > 1 || a.review
	return d.card(gtx, "ask", d.agent()+" has a question", func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var ws []layout.Widget
		if multiStep {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return d.stepBar(gtx, a, qs, step)
			})
		}
		if a.onReview() {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return d.askReview(gtx, in, a)
			})
		} else if step < len(qs) && step < len(a.opts) {
			q := qs[step]
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return vstack(gtx, gtx.Dp(theme.SpaceXS),
					func(gtx layout.Context) layout.Dimensions {
						if multiStep || q.Header == "" {
							return layout.Dimensions{}
						}
						return d.tag(gtx, q.Header)
					},
					func(gtx layout.Context) layout.Dimensions {
						return d.selectable(gtx, d.t.sel("q:"+in.ID+"/"+q.ID), tstyle{medium(th.UIFont), th.Sp(theme.Body), th.Fg, 1.45}, q.Prompt)
					},
					func(gtx layout.Context) layout.Dimensions {
						if !q.Multi {
							return layout.Dimensions{}
						}
						return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, "Pick any")
					},
				)
			})
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return d.askOptions(gtx, a, step, q, true)
			})
		}
		if a.note != "" {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return d.selectable(gtx, d.t.sel("qnote:"+in.ID), d.small(th.Readable(th.Red, th.Surface)), a.note)
			})
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return d.askFooter(gtx, a, qs, step, multiStep)
		})
		return vstack(gtx, gtx.Dp(theme.SpaceM), ws...)
	})
}

// stepBar draws the pill chips for every question (and the Submit chip
// when the card mirrors a final review step), clickable to jump steps.
func (d *cdraw) stepBar(gtx layout.Context, a *ask, qs []session.Question, step int) layout.Dimensions {
	th := d.th
	var chips []layout.Widget
	allNamed := true
	for _, q := range qs {
		if strings.TrimSpace(q.Header) == "" {
			allNamed = false
			break
		}
	}
	for i, q := range qs {
		if i >= len(a.chips) {
			break
		}
		lbl := strings.TrimSpace(q.Header)
		if lbl == "" {
			if i == step {
				lbl = "Question " + strconv.Itoa(i+1) + " of " + strconv.Itoa(len(qs))
			} else {
				lbl = "Question " + strconv.Itoa(i+1)
			}
		}
		cur := i == step
		done := a.stepDone(qs, i)
		chips = append(chips, func(gtx layout.Context) layout.Dimensions {
			return d.stepChip(gtx, &a.chips[i], lbl, cur, done)
		})
	}
	if a.review && len(qs) < len(a.chips) {
		_, ready := a.answers(qs)
		chips = append(chips, func(gtx layout.Context) layout.Dimensions {
			return d.stepChip(gtx, &a.chips[len(qs)], "Submit", step == len(qs), ready)
		})
	}
	if allNamed && len(qs) > 1 && step < len(qs) {
		counter := strconv.Itoa(step+1) + " of " + strconv.Itoa(len(qs))
		chips = append(chips, func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(24)
			c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Caption), th.Muted, counter)
			})
			at(gtx, image.Pt(gtx.Dp(theme.SpaceXS), (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions {
				c.Add(gtx.Ops)
				return td
			})
			return layout.Dimensions{Size: image.Pt(td.Size.X+gtx.Dp(theme.SpaceXS), h)}
		})
	}
	return wrapRow(gtx, gtx.Dp(theme.SpaceS), chips...)
}

// stepChip is one small pill in the question step header.
func (d *cdraw) stepChip(gtx layout.Context, c *widget.Clickable, txt string, current, done bool) layout.Dimensions {
	th := d.th
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(24)
		px := gtx.Dp(theme.SpaceS + theme.Space2XS)
		f := th.UIFont
		fill, ring, fg := th.Bg, th.BorderSubtle, th.Muted
		switch {
		case current:
			f = medium(th.UIFont)
			fill, ring, fg = th.SelectedBg, theme.Mix(th.SelectedBg, th.Primary, 0.55), th.Fg
		case done:
			fill, ring, fg = th.SurfaceSecondary, th.BorderSubtle, th.Fg
		}
		if c.Pressed() {
			fill = th.Pressed
		} else if c.Hovered() && !current {
			fill = th.Hover
		}
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, f, th.Sp(theme.Small), fg, txt)
		})
		isz := gtx.Dp(11)
		gap := gtx.Dp(theme.SpaceXS)
		w := px*2 + ld.Size.X
		if done {
			w += isz + gap
		}
		rrect(gtx, image.Rect(0, 0, w, h), h/2, fill, ring)
		x := px
		if done {
			icCol := th.Readable(th.Green, fill)
			if current {
				icCol = th.Readable(th.Primary, fill)
			}
			at(gtx, image.Pt(x, (h-isz)/2), func(gtx layout.Context) layout.Dimensions {
				return sidebar.Icon(gtx, "check", isz, icCol)
			})
			x += isz + gap
		}
		at(gtx, image.Pt(x, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions {
			lc.Add(gtx.Ops)
			return ld
		})
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// askReview is the final Review step mirroring Claude Code's Submit tab:
// a compact summary of every question and its answer, clickable to edit.
func (d *cdraw) askReview(gtx layout.Context, in session.InputRequest, a *ask) layout.Dimensions {
	th := d.th
	return vstack(gtx, gtx.Dp(theme.SpaceS),
		func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Review your answers")
		},
		func(gtx layout.Context) layout.Dimensions {
			w := gtx.Constraints.Max.X
			pad := gtx.Dp(theme.SpaceS)
			var rows []layout.Widget
			for i, q := range in.Questions {
				if i >= len(a.revRows) {
					break
				}
				vals, ok := a.answerFor(q, i)
				if !ok && a.lastTUIAns != nil {
					if v := a.lastTUIAns[q.ID]; len(v) > 0 {
						vals, ok = v, true
					}
				}
				ansText := "Unanswered"
				ansCol := th.Readable(th.Yellow, th.Bg)
				if ok {
					ansText = strings.Join(vals, ", ")
					ansCol = th.Fg
				}
				title := q.Prompt
				if title == "" {
					title = cmp.Or(q.Header, "Question "+strconv.Itoa(i+1))
				}
				rows = append(rows, func(gtx layout.Context) layout.Dimensions {
					c := &a.revRows[i]
					return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
						rw := gtx.Constraints.Max.X
						px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS)
						g := gtx
						g.Constraints = layout.Constraints{Max: image.Pt(max(0, rw-2*px), gtx.Constraints.Max.Y)}
						call, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
							return vstack(gtx, gtx.Dp(2),
								func(gtx layout.Context) layout.Dimensions {
									return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, title, 0)
								},
								func(gtx layout.Context) layout.Dimensions {
									return label(gtx, th, medium(th.UIFont), th.Sp(theme.Body), ansCol, ansText, 0)
								},
							)
						})
						h := bd.Size.Y + 2*py
						if c.Pressed() {
							paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rect(0, 0, rw, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
						} else if c.Hovered() {
							paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, rw, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
						}
						at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return bd })
						return layout.Dimensions{Size: image.Pt(rw, h)}
					})
				})
			}
			g := gtx
			g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*pad), gtx.Constraints.Max.Y)}
			call, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
				return vstack(gtx, gtx.Dp(theme.Space2XS), rows...)
			})
			h := bd.Size.Y + 2*pad
			rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), th.Bg, th.BorderSubtle)
			at(gtx, image.Pt(pad, pad), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return bd })
			return layout.Dimensions{Size: image.Pt(w, h)}
		},
	)
}

// askFooter lays out Back on the left (when past step 0) and a primary
// Next or Submit button on the right.
func (d *cdraw) askFooter(gtx layout.Context, a *ask, qs []session.Question, step int, multiStep bool) layout.Dimensions {
	w := gtx.Constraints.Max.X
	var leftCall op.CallOp
	var leftDim layout.Dimensions
	if multiStep && step > 0 {
		leftCall, leftDim = measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.keyButton(gtx, &a.back, false, "Back", "←", true)
		})
	}
	var rightBtn *widget.Clickable
	var rightTxt, rightKey string
	var rightOn bool
	switch {
	case a.onReview():
		_, ready := a.answers(qs)
		if !ready && a.hasTUI {
			ready = true // TUI is already on its Review screen
		}
		rightBtn, rightTxt, rightKey, rightOn = &a.submit, "Submit", "Enter", ready
	case !multiStep:
		_, ready := a.answers(qs)
		txt := "Answer"
		if qs[0].Multi {
			txt, rightKey = "Submit", "Enter"
		}
		rightBtn, rightTxt, rightOn = &a.submit, txt, ready
	case step == len(qs)-1 && !a.review:
		_, curOK := a.answerFor(qs[step], step)
		_, allOK := a.answers(qs)
		rightBtn, rightTxt, rightKey, rightOn = &a.submit, "Submit", "Enter", curOK && (allOK || a.hasTUI)
	default:
		_, curOK := a.answerFor(qs[step], step)
		rightBtn, rightTxt, rightKey, rightOn = &a.next, "Next", "Enter", curOK
	}
	rightCall, rightDim := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.keyButton(gtx, rightBtn, true, rightTxt, rightKey, rightOn)
	})
	h := max(leftDim.Size.Y, rightDim.Size.Y)
	if leftDim.Size.X > 0 {
		at(gtx, image.Pt(0, (h-leftDim.Size.Y)/2), func(gtx layout.Context) layout.Dimensions {
			leftCall.Add(gtx.Ops)
			return leftDim
		})
	}
	rx := max(leftDim.Size.X+gtx.Dp(theme.SpaceS), w-rightDim.Size.X)
	if leftDim.Size.X == 0 {
		rx = max(0, w-rightDim.Size.X)
	}
	at(gtx, image.Pt(rx, (h-rightDim.Size.Y)/2), func(gtx layout.Context) layout.Dimensions {
		rightCall.Add(gtx.Ops)
		return rightDim
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// askOptions are question qi's option rows and its typed row; with
// previews, the rows on the left and the hovered or picked option's
// preview on the right, or under them when the card is narrow.
func (d *cdraw) askOptions(gtx layout.Context, a *ask, qi int, q session.Question, keys bool) layout.Dimensions {
	th := d.th
	multi := a.multiQ(qi)
	rows := func(gtx layout.Context) layout.Dimensions {
		var rs []layout.Widget
		for oi, o := range q.Options {
			on := a.pick[qi] == oi
			if multi != nil {
				on = multi[oi]
			}
			rs = append(rs, func(gtx layout.Context) layout.Dimensions {
				return d.option(gtx, &a.opts[qi][oi], on, multi != nil, keyFor(keys, oi), func(gtx layout.Context) layout.Dimensions {
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
		otherOn := a.pick[qi] == n
		if multi != nil {
			otherOn = strings.TrimSpace(a.other[qi].Text()) != ""
		}
		rs = append(rs, func(gtx layout.Context) layout.Dimensions {
			return d.option(gtx, &a.opts[qi][n], otherOn, multi != nil, keyFor(keys, n), func(gtx layout.Context) layout.Dimensions {
				return d.otherField(gtx, &a.other[qi])
			})
		})
		return vstack(gtx, gtx.Dp(theme.SpaceXS), rs...)
	}
	preview := ""
	hovered := ""
	for oi, o := range q.Options {
		if o.Preview == "" {
			continue
		}
		if oi < len(a.opts[qi]) && a.opts[qi][oi].Hovered() {
			hovered = o.Preview
		}
		if preview == "" || a.pick[qi] == oi {
			preview = o.Preview
		}
	}
	if hovered != "" {
		preview = hovered
	}
	if preview == "" {
		return rows(gtx)
	}
	box := func(gtx layout.Context) layout.Dimensions {
		return d.monoBox(gtx, "preview:"+q.ID, mdview.Detab(strings.TrimRight(preview, "\n")))
	}
	w := gtx.Constraints.Max.X
	if w < gtx.Dp(520) {
		return vstack(gtx, gtx.Dp(theme.SpaceS), rows, box)
	}
	gap := gtx.Dp(theme.SpaceM)
	lw := (w - gap) * 45 / 100
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(lw, gtx.Constraints.Max.Y)}
	lc, ld := measure(g, rows)
	g.Constraints = layout.Constraints{Max: image.Pt(w-lw-gap, gtx.Constraints.Max.Y)}
	pc, pd := measure(g, box)
	lc.Add(gtx.Ops)
	at(gtx, image.Pt(lw+gap, 0), func(gtx layout.Context) layout.Dimensions { pc.Add(gtx.Ops); return pd })
	return layout.Dimensions{Size: image.Pt(w, max(ld.Size.Y, pd.Size.Y))}
}

// option is one of a question's rows: its number on a keycap, a checkbox
// when several may be picked, then body.
func (d *cdraw) option(gtx layout.Context, c *widget.Clickable, on, check bool, k string, body layout.Widget) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS)
		lead := gtx.Dp(32)
		if check {
			lead += gtx.Dp(24)
		}
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px-lead), gtx.Constraints.Max.Y)}
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
		if k != "" { // only the question the keys act on shows them
			at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { return d.keycap(gtx, k, false) })
		}
		if check {
			bx, bs := px+gtx.Dp(32), gtx.Dp(16)
			r := image.Rect(bx, py+gtx.Dp(1), bx+bs, py+gtx.Dp(1)+bs)
			if on {
				paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(r, gtx.Dp(theme.SpaceXS)).Op(gtx.Ops))
				icon(gtx, r.Min.Add(image.Pt(gtx.Dp(theme.Space2XS), gtx.Dp(theme.Space2XS))), "check", 12, th.OnPrimary)
			} else {
				rrect(gtx, r, gtx.Dp(theme.SpaceXS), th.Bg, th.BorderStrong)
			}
		}
		at(gtx, image.Pt(px+lead, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return bd })
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

// resolvedLine is a request answered here: a check, or an x for a denial,
// the decision, what it was for and when.
func (d *cdraw) resolvedLine(gtx layout.Context, r *resolved, open bool) layout.Dimensions {
	th := d.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(40)
	var body op.CallOp
	var bd layout.Dimensions
	more := r.details()
	if open && more {
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*gtx.Dp(cardPadX)), gtx.Constraints.Max.Y)}
		body, bd = measure(g, func(gtx layout.Context) layout.Dimensions { return d.resolvedBody(gtx, r) })
	}
	total := h
	if bd.Size.Y > 0 {
		total += bd.Size.Y + gtx.Dp(cardPadX)
	}
	rrect(gtx, image.Rect(0, 0, w, total), gtx.Dp(10), th.Bg, th.BorderStrong)
	head := func(gtx layout.Context) layout.Dimensions {
		x := gtx.Dp(cardPadX)
		if more {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(x, (h-gtx.Dp(12))/2), chev, 12, th.Muted)
			x += gtx.Dp(12) + gtx.Dp(theme.SpaceS)
		}
		icon(gtx, image.Pt(x, (h-gtx.Dp(14))/2), resolvedIcon(r), 14, th.Muted)
		x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
		tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			if r.when.IsZero() {
				return layout.Dimensions{}
			}
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
			if r.prose {
				f = th.UIFont
			}
			wc, wd := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, f, th.Sp(theme.Small), th.TextQuiet, firstLine(r.what))
			})
			at(gtx, image.Pt(x, onBase(base, wd)), func(gtx layout.Context) layout.Dimensions { wc.Add(gtx.Ops); return wd })
		}
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	if more {
		c := d.t.click("res:" + r.id)
		clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
			rowFill(gtx, th, c, image.Pt(w, h))
			return head(gtx)
		})
	} else {
		head(gtx)
	}
	if bd.Size.Y > 0 {
		at(gtx, image.Pt(gtx.Dp(cardPadX), h), func(gtx layout.Context) layout.Dimensions { body.Add(gtx.Ops); return bd })
	}
	return layout.Dimensions{Size: image.Pt(w, total)}
}

// details is whether r opens: a question's questions, or an approval's
// command, edit or reason.
func (r *resolved) details() bool {
	return len(r.qs) > 0 || r.cmd != "" || r.diff != "" || r.reason != ""
}

// resolvedBody is an open resolved line: each question with its options,
// the picked ones checked, a typed answer, and the picked option's
// preview; or the command or edit allowed or denied, and the reason.
func (d *cdraw) resolvedBody(gtx layout.Context, r *resolved) layout.Dimensions {
	th := d.th
	var ws []layout.FlexChild
	text := func(key string, st tstyle, s string) {
		ws = append(ws, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d.selectable(gtx, d.t.sel(key), st, s) }))
	}
	box := func(key, s string) {
		ws = append(ws, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: theme.Space2XS, Bottom: theme.Space2XS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return d.monoBox(gtx, key, s) })
		}))
	}
	key := "res:" + r.id + ":"
	for qi, q := range r.qs {
		k := key + strconv.Itoa(qi)
		if q.header != "" {
			text(k+"h", d.small(th.Muted), q.header)
		}
		text(k+"p", d.small(th.Fg), q.prompt)
		typed := slices.Clone(q.picked)
		preview := ""
		for oi, o := range q.options {
			mark, c := "   ", th.Muted
			if slices.Contains(q.picked, o.Label) {
				mark, c = "✓ ", th.Fg
				typed = slices.DeleteFunc(typed, func(p string) bool { return p == o.Label })
				preview = cmp.Or(preview, o.Preview)
			}
			line := mark + o.Label
			if o.Description != "" {
				line += " · " + o.Description
			}
			text(k+"o"+strconv.Itoa(oi), d.small(c), line)
		}
		for ti, v := range typed {
			text(k+"t"+strconv.Itoa(ti), d.small(th.Fg), "✓ "+v)
		}
		if preview != "" {
			box(k+"v", preview)
		}
	}
	if r.cmd != "" {
		box(key+"c", r.cmd)
	}
	if r.diff != "" {
		box(key+"d", r.diff)
	}
	if r.reason != "" {
		text(key+"r", d.small(th.Fg), "Reason: "+r.reason)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
}

func resolvedIcon(r *resolved) string {
	if r.denied {
		return "x"
	}
	return "check"
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

// keyFor is option i's key on its keycap, "" when the keys act on
// another question.
func keyFor(keys bool, i int) string {
	if !keys || i >= 9 {
		return ""
	}
	return strconv.Itoa(i + 1)
}
