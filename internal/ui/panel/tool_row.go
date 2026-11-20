package panel

import (
	"image"
	"image/color"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/syntax"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// outTail is how many lines of a command's output an opened row shows
// before Show all.
const outTail = 12

// bodyIndent is where a tool row's text starts, and its opened body
// with it: the side padding, the chevron, the icon and their gaps.
const bodyIndent = rowPad + 14 + theme.SpaceS + 14 + theme.SpaceS

// diffHead is how many lines of an edit an opened row shows before Show
// all; createdHead, of a file a Write made; readHead, of a file read.
const (
	diffHead    = 60
	createdHead = 20
	readHead    = 40
	listHead    = 12
)

// diffCache is an edit's lines, worked out once per update of its item.
type diffCache struct {
	updated string
	lines   []diffLine
}

// diff is it's edit as lines, cached.
func (d *cdraw) diff(it *session.Item) []diffLine {
	if d.t.diffs == nil {
		d.t.diffs = map[string]diffCache{}
	}
	c, ok := d.t.diffs[it.ID]
	if !ok || c.updated != it.UpdatedAt {
		c = diffCache{updated: it.UpdatedAt, lines: diffLines(it.Tool)}
		d.t.diffs[it.ID] = c
	}
	return c.lines
}

// view is it's row and output, cached once the call finished.
func (d *cdraw) view(it *session.Item) (toolView, string) {
	ts := &d.t.tool
	if c, ok := ts.views[it.ID]; ok && c.updated == it.UpdatedAt && it.Tool.Status != "running" {
		return c.v, c.out
	}
	c := it.Tool
	out := cleanOutput(toolOutput(c))
	v := describe(c, summarize(c, d.diff(it), parseTime(it.CreatedAt), d.t.now), out)
	if ts.views == nil {
		ts.views = map[string]viewCache{}
	}
	ts.views[it.ID] = viewCache{updated: it.UpdatedAt, v: v, out: out}
	return v, out
}

// openTarget opens a row's link: a file on this machine in the side
// panel's File view, else a URL, or a path from the chat's folder, with
// Open.
func (t *Transcript) openTarget(target string) {
	if p := t.localFile(target); p != "" {
		t.showFile(p)
		return
	}
	if t.Open == nil || target == "" {
		return
	}
	// A rooted or drive path is never under the chat's folder, whichever OS
	// wrote it: /x on Windows, C:\x on Linux.
	rooted := filepath.IsAbs(target) || strings.HasPrefix(target, "/") || strings.HasPrefix(target, `\`) || drivePath.MatchString(target)
	if !strings.Contains(target, "://") && !rooted && t.Dir != "" {
		target = filepath.Join(t.Dir, target)
	}
	t.Open(target)
}

// drivePath is a Windows path from a drive's root, C:\ or C:/.
var drivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// linkClicked reports a click on a link inside row id's header, and
// drops the row's own click from the same press, so the link does not
// also fold the row.
func (d *cdraw) linkClicked(gtx layout.Context, row, link string) bool {
	hit := false
	for d.t.click(link).Clicked(gtx) {
		hit = true
	}
	if hit {
		for d.t.click(row).Clicked(gtx) {
		}
	}
	return hit
}

// toolRow is a call's row: a 30 dp header that says in one line what it
// did, the pictures it returned, and what a click opens under it. It
// needs only the item, so a subagent's card can draw its calls with it.
func (d *cdraw) toolRow(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	c := it.Tool
	v, out := d.view(it)
	link := v.link
	if v.path {
		v.arg = relPath(d.t.Dir, v.arg)
	}
	running, failed := c.Status == "running", c.Status == "failed"
	if running {
		d.t.live = true
	}
	if v.kind == "bash" { // its colors kept, for the box
		raw := c.Output
		if raw == "" && running {
			raw = c.Progress
		}
		out = cleanOutput(raw)
	}
	if shellID(c) != "" {
		var bgOut string
		v.bg, bgOut = d.shell(it)
		if bgOut != "" {
			out = bgOut
		}
		if c.ToolName == "Monitor" {
			out = monitorEvents(out)
		}
	}
	pr, hasPR := prIn(out, d.t.PRs)
	if d.linkClicked(gtx, it.ID, "link:"+it.ID) {
		d.t.openTarget(link)
	}
	if hasPR && d.linkClicked(gtx, it.ID, "pr:"+it.ID) {
		d.t.openTarget(pr.URL)
	}
	body := d.toolBody(it, v, out)
	last := len(d.s.Content) > 0 && it == &d.s.Content[len(d.s.Content)-1] && d.s.SessionState == session.StateRunning
	open := body != nil && d.t.toggled(gtx, it.ID, v.kind == "bash" && (running || last))
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	head := func(gtx layout.Context) layout.Dimensions {
		ck := d.t.click(it.ID)
		if body != nil {
			rowFill(gtx, th, ck, image.Pt(w, hh))
		}
		x := gtx.Dp(rowPad)
		if body != nil {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		}
		x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
		isz := gtx.Dp(14)
		switch {
		case running:
			at(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Bg, th.Blue, d.sec)
			})
		case failed:
			icon(gtx, image.Pt(x, (hh-isz)/2), "x", 14, th.Readable(th.Red, th.Bg))
		default:
			icon(gtx, image.Pt(x, (hh-isz)/2), v.icon, 14, th.Muted)
		}
		x += isz + gtx.Dp(theme.SpaceS)
		// One baseline for the row, from the body text's size.
		_, ref := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Ag")
		})
		base := centerBase(hh, ref)
		rc, rd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = w * 45 / 100
			return d.toolResult(gtx, it.ID, v, failed, pr, hasPR)
		})
		rx := w - gtx.Dp(rowPad) - rd.Size.X
		at(gtx, image.Pt(rx, onBase(base, rd)), func(gtx layout.Context) layout.Dimensions { rc.Add(gtx.Ops); return rd })
		if v.verb != "" {
			g := gtx
			g.Constraints.Max.X = max(0, rx-gtx.Dp(12)-x)
			vc, vd := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, v.verb)
			})
			at(gtx, image.Pt(x, onBase(base, vd)), func(gtx layout.Context) layout.Dimensions { vc.Add(gtx.Ops); return vd })
			x += vd.Size.X + gtx.Dp(theme.SpaceS)
		}
		room := rx - gtx.Dp(12) - x
		if room <= gtx.Dp(24) || v.arg == "" {
			return layout.Dimensions{Size: image.Pt(w, hh)}
		}
		var nc layout.Dimensions
		var ncall func()
		if v.note != "" {
			call, dims := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.TextQuiet, v.note)
			})
			if dims.Size.X < room/2 {
				nc, room = dims, room-dims.Size.X-gtx.Dp(theme.SpaceS)
				ncall = func() { call.Add(gtx.Ops) }
			}
		}
		g := gtx
		g.Constraints.Max.X = room
		var ad layout.Dimensions
		switch {
		case link != "":
			ad = at(gtx, image.Pt(x, (hh-gtx.Dp(22))/2), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = room
				return d.linkChip(gtx, d.t.click("link:"+it.ID), v.arg, v.path || v.kind == "read" || v.kind == "edit")
			})
		case v.mono:
			ac, dims := measure(g, func(gtx layout.Context) layout.Dimensions { return d.codeArg(gtx, v, room) })
			at(gtx, image.Pt(x, onBase(base, dims)), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return dims })
			ad = dims
		default:
			ac, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), theme.Mix(th.Fg, th.Bg, 0.3), v.arg)
			})
			at(gtx, image.Pt(x, onBase(base, dims)), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return dims })
			ad = dims
		}
		if ncall != nil {
			at(gtx, image.Pt(x+ad.Size.X+gtx.Dp(theme.SpaceS), onBase(base, nc)), func(gtx layout.Context) layout.Dimensions { ncall(); return nc })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	}
	var hd layout.Dimensions
	if body != nil {
		hd = clickable(gtx, d.t.click(it.ID), head)
	} else {
		hd = head(gtx)
	}
	y := hd.Size.Y
	if len(c.Images) > 0 {
		id := at(gtx, image.Pt(gtx.Dp(bodyIndent), y), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = max(0, w-gtx.Dp(bodyIndent)-gtx.Dp(rowPad))
			return d.toolImages(gtx, it)
		})
		y += id.Size.Y + gtx.Dp(10)
	}
	if !open {
		return layout.Dimensions{Size: image.Pt(w, y)}
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-gtx.Dp(bodyIndent)-gtx.Dp(rowPad)), gtx.Constraints.Max.Y)}
	bd := at(g, image.Pt(gtx.Dp(bodyIndent), y), body)
	return layout.Dimensions{Size: image.Pt(w, y+bd.Size.Y+gtx.Dp(10))}
}

// codeArg is a command or pattern in the header, in mono: a command after
// a quiet $.
func (d *cdraw) codeArg(gtx layout.Context, v toolView, room int) layout.Dimensions {
	th := d.th
	sz := th.Sp(theme.Small)
	fg := theme.Mix(th.Fg, th.Bg, 0.2)
	if v.verb != "" {
		fg = th.Muted
	}
	arg := v.arg
	if !v.prompt {
		return d.lbl(gtx, th.MonoFont, sz, fg, arg)
	}
	return hrow(gtx, 0,
		func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, th.MonoFont, sz, th.TextQuiet, "$ ") },
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = max(0, room-gtx.Sp(sz))
			return d.lbl(gtx, th.MonoFont, sz, fg, arg)
		})
}

// linkChip is a path or URL in a header that a click opens: a quiet chip,
// a path's folder dimmer than its file, brighter under the pointer.
func (d *cdraw) linkChip(gtx layout.Context, ck *widget.Clickable, txt string, path bool) layout.Dimensions {
	th := d.th
	return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		h, px := gtx.Dp(22), gtx.Dp(6)
		sz := th.Sp(theme.Body)
		room := max(0, gtx.Constraints.Max.X-2*px)
		if path {
			txt = middleCut(txt, room, gtx.Sp(sz)*55/100)
		}
		dir, file := "", txt
		if i := strings.LastIndexAny(txt, `/\`); path && i >= 0 && i < len(txt)-1 {
			dir, file = txt[:i+1], txt[i+1:]
		}
		hot := ck.Hovered()
		fg, dim := theme.Mix(th.Fg, th.Bg, 0.2), th.Muted
		if hot {
			fg, dim = th.Fg, theme.Mix(th.Muted, th.Fg, 0.4)
		}
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = room
			return hrow(gtx, 0,
				func(gtx layout.Context) layout.Dimensions {
					if dir == "" {
						return layout.Dimensions{}
					}
					return d.lbl(gtx, th.UIFont, sz, dim, dir)
				},
				func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, th.UIFont, sz, fg, file) })
		})
		w := ld.Size.X + 2*px
		fill := theme.Mix(th.Bg, th.Fg, 0.05)
		if hot {
			fill = theme.Mix(th.Bg, th.Fg, 0.10)
		}
		r := image.Rect(0, 0, w, h)
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(r, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		at(gtx, image.Pt(px, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		defer clip.Rect(r).Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// toolResult is a row's right end: a failed call's error, a background
// shell's state, a PR's live state, +N −M for an edit, passed and the
// time for a command.
func (d *cdraw) toolResult(gtx layout.Context, id string, v toolView, failed bool, pr model.PR, hasPR bool) layout.Dimensions {
	th := d.th
	f, sz := th.UIFont, th.Sp(theme.Small)
	part := func(c color.NRGBA, s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			if s == "" {
				return layout.Dimensions{}
			}
			return d.lbl(gtx, f, sz, c, s)
		}
	}
	var ws []layout.Widget
	if v.bg != "" {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.shellChip(gtx, v.bg) })
	}
	if hasPR {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.prChip(gtx, d.t.click("pr:"+id), pr) })
	}
	red := th.Readable(th.Red, th.Bg)
	switch {
	case failed:
		if v.errLine != "" && v.result != "failed" {
			ws = append(ws, part(red, v.result+" ·"))
		}
		msg := v.errLine
		if msg == "" {
			msg = v.result
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, f, sz, red, msg) })
	case v.add > 0 || v.del > 0:
		add, del := "+"+strconv.Itoa(v.add), "−"+strconv.Itoa(v.del)
		if v.del == 0 && v.created {
			del = ""
		}
		ws = append(ws, part(th.Readable(th.Green, th.Bg), add), part(red, del))
	case v.passed:
		ws = append(ws, part(th.Muted, v.result), func(gtx layout.Context) layout.Dimensions {
			return check(gtx, gtx.Dp(12), float32(gtx.Dp(1.6)), th.Readable(th.Green, th.Bg))
		})
	default:
		ws = append(ws, part(th.Muted, v.result))
	}
	return hrow(gtx, gtx.Dp(6), ws...)
}

// shellChip is a background shell's state: Running in background with a
// pulse while it runs, then how it ended.
func (d *cdraw) shellChip(gtx layout.Context, state string) layout.Dimensions {
	th := d.th
	label, c := "Running in background", th.Blue
	switch state {
	case "running":
		d.t.live = true
	case "completed":
		label, c = "Finished", th.Green
	case "failed":
		label, c = "Failed", th.Red
	case "killed":
		label, c = "Stopped", th.Muted
	default:
		label, c = state, th.Muted
	}
	bg, fg := th.Chip(c, th.Bg)
	return d.chip(gtx, bg, fg, label, func(gtx layout.Context) layout.Dimensions {
		dot := gtx.Dp(6)
		col := fg
		if state == "running" {
			col.A = uint8(90 + 165*pulse(d.sec))
		}
		circle(gtx, image.Pt(0, 0), dot, col)
		return layout.Dimensions{Size: image.Pt(dot, dot)}
	})
}

// chip is a small pill: a lead mark, then a label.
func (d *cdraw) chip(gtx layout.Context, bg, fg color.NRGBA, label string, lead layout.Widget) layout.Dimensions {
	th := d.th
	h, px := gtx.Dp(20), gtx.Dp(theme.SpaceS)
	lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return hrow(gtx, gtx.Dp(theme.SpaceXS), lead, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Caption), fg, label)
		})
	})
	w := ld.Size.X + 2*px
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	at(gtx, image.Pt(px, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
	return layout.Dimensions{Size: image.Pt(w, h), Baseline: (h-ld.Size.Y)/2 + ld.Baseline}
}

// prChip is a tracked PR's number and live state, its checks as a mark;
// a click opens it.
func (d *cdraw) prChip(gtx layout.Context, ck *widget.Clickable, pr model.PR) layout.Dimensions {
	th := d.th
	state, c := "Open", th.Green
	switch {
	case pr.State == model.PRMerged:
		state, c = "Merged", th.Purple
	case pr.State == model.PRClosed:
		state, c = "Closed", th.Red
	case pr.Draft:
		state, c = "Draft", th.Muted
	}
	bg, fg := th.Chip(c, th.Bg)
	if ck.Hovered() {
		bg = theme.Mix(bg, fg, 0.12)
	}
	return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		dims := d.chip(gtx, bg, fg, "#"+strconv.Itoa(pr.Number)+" "+state, func(gtx layout.Context) layout.Dimensions {
			s := gtx.Dp(10)
			switch pr.CI() {
			case model.CheckPass:
				return check(gtx, s, float32(gtx.Dp(1.6)), th.Readable(th.Green, bg))
			case model.CheckFail:
				return sidebarX(gtx, s, th.Readable(th.Red, bg))
			case model.CheckPending:
				circle(gtx, image.Pt(gtx.Dp(2), gtx.Dp(2)), gtx.Dp(6), th.Readable(th.Yellow, bg))
				return layout.Dimensions{Size: image.Pt(s, s)}
			}
			return layout.Dimensions{}
		})
		defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return dims
	})
}

// sidebarX is a failed check's x, s px square.
func sidebarX(gtx layout.Context, s int, c color.NRGBA) layout.Dimensions {
	icon(gtx, image.Point{}, "x", 10, c)
	return layout.Dimensions{Size: image.Pt(s, s)}
}

// prIn is the tracked PR whose URL out names, the lowest numbered when
// it names several.
func prIn(out string, prs map[string]model.PR) (model.PR, bool) {
	if len(prs) == 0 || !strings.Contains(out, "/pull/") {
		return model.PR{}, false
	}
	var found []model.PR
	for _, pr := range prs {
		if pr.URL == "" {
			continue
		}
		for rest := out; ; {
			i := strings.Index(rest, pr.URL)
			if i < 0 {
				break
			}
			rest = rest[i+len(pr.URL):]
			if rest == "" || rest[0] < '0' || rest[0] > '9' { // /pull/45 is not /pull/458
				found = append(found, pr)
				break
			}
		}
	}
	if len(found) == 0 {
		return model.PR{}, false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Number < found[j].Number })
	return found[0], true
}

// shell is a background shell's state and latest output, from its call
// and the calls that read or stopped it.
func (d *cdraw) shell(it *session.Item) (string, string) {
	sh := &d.t.tool.shells
	sh.index(d.s.Content)
	var att []*session.ToolCall
	for _, i := range sh.calls[it.ID] {
		att = append(att, d.s.Content[i].Tool)
	}
	state, out := shellState(it.Tool, att)
	if state == "running" && d.s.SessionState != session.StateRunning && d.s.SessionState != session.StateWaiting && d.s.SessionState != session.StateReady {
		state = "killed" // the chat ended, and its shells with it
	}
	return state, cleanOutput(out)
}

// toolBody is what an opened row shows, nil when it has nothing to show.
func (d *cdraw) toolBody(it *session.Item, v toolView, out string) layout.Widget {
	c := it.Tool
	id := it.ID
	switch c.ToolName {
	case "Task", "Agent", "TodoWrite": // drawn by their own cards
		if out == "" {
			return nil
		}
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, id, "", out, false) }
	}
	switch {
	case v.kind == "edit":
		lines := d.diff(it)
		if len(lines) == 0 {
			return d.textBody(id, out)
		}
		return func(gtx layout.Context) layout.Dimensions { return d.diffBox(gtx, id, v, lines) }
	case v.kind == "bash":
		cmd := c.Command
		if cmd == "" {
			cmd = inputMap(c.Input).str("command", "cmd")
		}
		if cmd == "" {
			cmd = v.arg
		}
		if c.Status == "failed" && c.ExitCode != nil {
			if first, rest, ok := strings.Cut(out, "\n"); ok && exitCodeLine.MatchString(first) {
				out = rest
			} else if exitCodeLine.MatchString(out) {
				out = ""
			}
		}
		live := c.Status == "running" || v.bg == "running"
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, id, cmd, out, live) }
	case v.kind == "read":
		if len(c.Images) > 0 && strings.TrimSpace(out) == "" {
			return nil
		}
		if v.readNo {
			lines := readLines(out)
			var plain []string
			for _, l := range lines {
				plain = append(plain, l.text)
			}
			b := monoBox{id: id, lines: lines, nums: 1, head: readHead, copy: strings.Join(plain, "\n")}
			d.highlight(id, syntax.FromPath(v.arg), b.lines, false)
			return func(gtx layout.Context) layout.Dimensions { return d.box(gtx, b) }
		}
		return d.textBody(id, out)
	case len(v.hits) > 0:
		return func(gtx layout.Context) layout.Dimensions { return d.fileList(gtx, id, v.hits) }
	case len(v.sources) > 0:
		return func(gtx layout.Context) layout.Dimensions { return d.sourceList(gtx, id, v.sources) }
	case c.ToolName == "WebFetch" && out != "":
		item := &session.Item{ID: "fetch:" + id, Text: out}
		return func(gtx layout.Context) layout.Dimensions {
			return d.markdown(gtx, item.ID, d.t.reply(item, false))
		}
	case c.ToolName == "Skill":
		return nil
	case v.kind == "search", c.ToolName == "LS", c.ToolName == "WebSearch":
		return d.textBody(id, out)
	}
	// MCP and tools pitwall does not know: their arguments and result.
	args := prettyJSON(string(c.Input))
	if t := strings.TrimSpace(args); t == "{}" || t == "null" || t == "" {
		args = ""
	}
	res := prettyJSON(out)
	if args == "" && strings.TrimSpace(res) == "" {
		return nil
	}
	return func(gtx layout.Context) layout.Dimensions {
		var ws []layout.Widget
		if args != "" {
			ws = append(ws, d.section("Arguments"), func(gtx layout.Context) layout.Dimensions {
				return d.box(gtx, monoBox{id: "args:" + id, lines: textMono(args), head: diffHead, copy: args})
			})
		}
		if strings.TrimSpace(res) != "" {
			ws = append(ws, d.section("Result"), func(gtx layout.Context) layout.Dimensions {
				return d.box(gtx, monoBox{id: id, lines: textMono(res), head: diffHead, copy: res})
			})
		}
		return vstack(gtx, gtx.Dp(6), ws...)
	}
}

// section is a small label over a part of a row's body.
func (d *cdraw) section(s string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(d.th.UIFont), d.th.Sp(theme.Caption), d.th.Muted, s)
		})
	}
}

// textBody is plain output in a box, nil when there is none.
func (d *cdraw) textBody(id, out string) layout.Widget {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return func(gtx layout.Context) layout.Dimensions {
		return d.box(gtx, monoBox{id: id, lines: textMono(out), head: diffHead, copy: out})
	}
}

// outBox is a command and its output in a box, its colors kept: the last
// lines first with Show all, and Copy. A live call with nothing printed
// yet shows just its command, not "(no output)".
func (d *cdraw) outBox(gtx layout.Context, id, cmd, out string, live bool) layout.Dimensions {
	var lines []monoLine
	if cmd != "" {
		lines = commandMono(cmd)
	}
	fixed := len(lines)
	if strings.TrimSpace(out) != "" {
		lines = append(lines, ansiLines(strings.TrimRight(out, "\n "))...)
	} else if !live {
		lines = append(lines, monoLine{kind: 'q', text: "(no output)"})
	}
	copyText := plainOutput(out)
	if cmd != "" {
		copyText = "$ " + cmd + "\n" + copyText
	}
	return d.box(gtx, monoBox{id: id, lines: lines, fixed: fixed, tail: outTail, copy: strings.TrimRight(copyText, "\n")})
}

// diffBox is an edit's hunks: line numbers in the gutter, each hunk's
// header, the added and removed lines on their tints, Copy diff on top. A
// new file shows its first lines.
func (d *cdraw) diffBox(gtx layout.Context, id string, v toolView, lines []diffLine) layout.Dimensions {
	whole := lines
	title := v.arg
	mono := false
	if len(lines) > 0 && lines[0].kind == '@' {
		title, mono, lines = lines[0].text, true, lines[1:]
	}
	b := monoBox{id: id, lines: diffMono(lines), nums: 2, signs: true, head: diffHead, title: title, titleMono: mono,
		copy: unifiedText(v.arg, whole), copyLabel: "Copy diff"}
	if v.created {
		b.nums, b.head = 1, createdHead
		b.title, b.titleMono = "New file · "+plural(len(lines), "line"), false
		b.copyLabel, b.copy = "Copy", newText(lines)
	}
	d.highlight(id, syntax.FromPath(v.arg), b.lines, !v.created)
	return d.box(gtx, b)
}

// newText is a new file's lines as its text.
func newText(lines []diffLine) string {
	var s []string
	for _, l := range lines {
		s = append(s, l.text)
	}
	return strings.Join(s, "\n")
}

// rowFill is a clickable row's Hover fill under the pointer, Pressed while
// held.
func rowFill(gtx layout.Context, th *theme.Theme, c *widget.Clickable, size image.Point) {
	switch {
	case c.Pressed():
		paint.FillShape(gtx.Ops, th.Pressed, clip.Rect{Max: size}.Op())
	case c.Hovered():
		paint.FillShape(gtx.Ops, th.Hover, clip.Rect{Max: size}.Op())
	}
}
