package sidebar

import (
	"image"
	"image/color"
	"math"
	"strconv"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// Lucide icons (ISC, lucide-react 1.23.0, the set aide imports), as SVG path
// data in lucide's 24x24 box with stroke width 2. Circles, lines and rects
// are rewritten as paths.
const (
	icCircleAlert = "M2 12a10 10 0 1 0 20 0a10 10 0 1 0-20 0M12 8v4M12 16h.01"
	icCircleHelp  = "M2 12a10 10 0 1 0 20 0a10 10 0 1 0-20 0M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3M12 17h.01"
	icCircleCheck = "M2 12a10 10 0 1 0 20 0a10 10 0 1 0-20 0M9 12l2 2 4-4"
	icGitBranch   = "M15 6a9 9 0 0 0-9 9V3M15 6a3 3 0 1 0 6 0a3 3 0 1 0-6 0M3 18a3 3 0 1 0 6 0a3 3 0 1 0-6 0"
	icFolder      = "M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"
	icLoader      = "M21 12a9 9 0 1 1-6.219-8.56"
	icTerminal    = "M12 19h8M4 17l6-6-6-6"
	icPlus        = "M5 12h14M12 5v14"
	icEllipsis    = "M11 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0M18 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0M4 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0"
	icSettings    = "M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915M9 12a3 3 0 1 0 6 0a3 3 0 1 0-6 0"
	icDetach      = "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" // lucide log-out
	icPower       = "M12 2v10M18.4 6.6a9 9 0 1 1-12.77.04"
	icFolderKanb  = "M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2ZM8 10v4M12 10v2M16 10v6"
	icMessage     = "M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z"
	icPencil      = "M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497zM15 5l4 4"
	icTrash       = "M10 11v6M14 11v6M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
)

// seg is one absolute path command in the 24x24 box: 'M', 'L', 'C' (pts
// holds ctrl0, ctrl1, end), 'A' (pts holds center, end; angle is the sweep)
// or 'Z'.
type seg struct {
	op    byte
	pts   [3]f32.Point
	angle float32
}

var iconCache = map[string][]seg{}

// parsePath turns SVG path data into absolute segments. It covers what lucide
// uses: M L H V C A Z and their relative forms, circular arcs only.
func parsePath(d string) []seg {
	var out []seg
	i := 0
	skip := func() {
		for i < len(d) && (d[i] == ' ' || d[i] == ',') {
			i++
		}
	}
	num := func() float32 {
		skip()
		j := i
		if j < len(d) && (d[j] == '-' || d[j] == '+') {
			j++
		}
		dot := false
		for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !dot) {
			if d[j] == '.' {
				dot = true
			}
			j++
		}
		v, _ := strconv.ParseFloat(d[i:j], 32)
		i = j
		return float32(v)
	}
	flag := func() bool {
		skip()
		i++
		return d[i-1] == '1'
	}
	var pen, start f32.Point
	var cmd byte
	for skip(); i < len(d); skip() {
		if c := d[i]; c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			cmd = c
			i++
			if cmd == 'Z' || cmd == 'z' {
				out = append(out, seg{op: 'Z'})
				pen = start
				continue
			}
		}
		rel := cmd >= 'a'
		pt := func() f32.Point {
			p := f32.Pt(num(), num())
			if rel {
				p = p.Add(pen)
			}
			return p
		}
		switch cmd | 0x20 {
		case 'm':
			pen = pt()
			start = pen
			out = append(out, seg{op: 'M', pts: [3]f32.Point{pen}})
			// Further pairs after a moveto are linetos.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			pen = pt()
			out = append(out, seg{op: 'L', pts: [3]f32.Point{pen}})
		case 'h':
			x := num()
			if rel {
				x += pen.X
			}
			pen.X = x
			out = append(out, seg{op: 'L', pts: [3]f32.Point{pen}})
		case 'v':
			y := num()
			if rel {
				y += pen.Y
			}
			pen.Y = y
			out = append(out, seg{op: 'L', pts: [3]f32.Point{pen}})
		case 'c':
			c0, c1, to := pt(), pt(), pt()
			pen = to
			out = append(out, seg{op: 'C', pts: [3]f32.Point{c0, c1, to}})
		case 'a':
			r := num()
			num() // ry: lucide arcs are circular
			num() // x-axis rotation
			large, sweep := flag(), flag()
			to := pt()
			c, a := arcCenter(pen, to, r, large, sweep)
			pen = to
			out = append(out, seg{op: 'A', pts: [3]f32.Point{c, to}, angle: a})
		default:
			panic("sidebar: unsupported path command " + string(cmd))
		}
	}
	return out
}

// arcCenter converts an SVG circular arc from p0 to p1 into its center and
// signed sweep angle (SVG spec F.6.5 with rx == ry and no rotation).
func arcCenter(p0, p1 f32.Point, r float32, large, sweep bool) (f32.Point, float32) {
	hx, hy := float64(p0.X-p1.X)/2, float64(p0.Y-p1.Y)/2
	rr := float64(r)
	if l := (hx*hx + hy*hy) / (rr * rr); l > 1 {
		rr *= math.Sqrt(l)
	}
	k := math.Sqrt(math.Max(0, (rr*rr-hx*hx-hy*hy)/(hx*hx+hy*hy)))
	if large == sweep {
		k = -k
	}
	cx := k*hy + float64(p0.X+p1.X)/2
	cy := -k*hx + float64(p0.Y+p1.Y)/2
	a0 := math.Atan2(float64(p0.Y)-cy, float64(p0.X)-cx)
	a1 := math.Atan2(float64(p1.Y)-cy, float64(p1.X)-cx)
	da := a1 - a0
	if sweep && da < 0 {
		da += 2 * math.Pi
	} else if !sweep && da > 0 {
		da -= 2 * math.Pi
	}
	return f32.Pt(float32(cx), float32(cy)), float32(da)
}

// drawIcon strokes icon d in a size x size px box at the current offset.
// rot spins it around its center (radians), for the connecting loader.
func drawIcon(gtx layout.Context, d string, size int, col color.NRGBA, rot float32) layout.Dimensions {
	segs, ok := iconCache[d]
	if !ok {
		segs = parsePath(d)
		iconCache[d] = segs
	}
	s := float32(size) / 24
	if rot != 0 {
		c := f32.Pt(float32(size)/2, float32(size)/2)
		defer op.Affine(f32.AffineId().Rotate(c, rot)).Push(gtx.Ops).Pop()
	}
	var p clip.Path
	p.Begin(gtx.Ops)
	for _, sg := range segs {
		a, b, c := sg.pts[0].Mul(s), sg.pts[1].Mul(s), sg.pts[2].Mul(s)
		switch sg.op {
		case 'M':
			p.MoveTo(a)
		case 'L':
			p.LineTo(a)
		case 'C':
			p.CubeTo(a, b, c)
		case 'A':
			p.ArcTo(a, a, sg.angle)
		case 'Z':
			p.Close()
		}
	}
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: 2 * s}.Op())
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// projectIcons are the icons the project popover offers, in grid order:
// lucide names (what model.Project.Icon holds) with their path data,
// generated from lucide-react 1.23.0 like the constants above.
var projectIcons = []struct{ name, d string }{
	{"folder", "M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"},
	{"code", "M0 0m16 18 6-6-6-6M0 0m8 6-6 6 6 6"},
	{"layers", "M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83zM2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17"},
	{"sparkles", "M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594zM20 2v4M22 4h-4M2 20a2 2 0 1 0 4 0a2 2 0 1 0 -4 0"},
	{"terminal", "M12 19h8M0 0m4 17 6-6-6-6"},
	{"box", "M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16ZM0 0m3.3 7 8.7 5 8.7-5M12 22V12"},
	{"package", "M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73zM12 22V12M3.29 7L12 12L20.71 7M0 0m7.5 4.27 9 5.15"},
	{"server", "M4 2h16a2 2 0 0 1 2 2v4a2 2 0 0 1 -2 2h-16a2 2 0 0 1 -2 -2v-4a2 2 0 0 1 2 -2ZM4 14h16a2 2 0 0 1 2 2v4a2 2 0 0 1 -2 2h-16a2 2 0 0 1 -2 -2v-4a2 2 0 0 1 2 -2ZM6 6L6.01 6M6 18L6.01 18"},
	{"globe", "M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20M2 12h20"},
	{"cpu", "M12 20v2M12 2v2M17 20v2M17 2v2M2 12h2M2 17h2M2 7h2M20 12h2M20 17h2M20 7h2M7 20v2M7 2v2M6 4h12a2 2 0 0 1 2 2v12a2 2 0 0 1 -2 2h-12a2 2 0 0 1 -2 -2v-12a2 2 0 0 1 2 -2ZM9 8h6a1 1 0 0 1 1 1v6a1 1 0 0 1 -1 1h-6a1 1 0 0 1 -1 -1v-6a1 1 0 0 1 1 -1Z"},
	{"bot", "M12 8V4H8M6 8h12a2 2 0 0 1 2 2v8a2 2 0 0 1 -2 2h-12a2 2 0 0 1 -2 -2v-8a2 2 0 0 1 2 -2ZM2 14h2M20 14h2M15 13v2M9 13v2"},
	{"brain", "M12 18V5M15 13a4.17 4.17 0 0 1-3-4 4.17 4.17 0 0 1-3 4M17.598 6.5A3 3 0 1 0 12 5a3 3 0 1 0-5.598 1.5M17.997 5.125a4 4 0 0 1 2.526 5.77M18 18a4 4 0 0 0 2-7.464M19.967 17.483A4 4 0 1 1 12 18a4 4 0 1 1-7.967-.517M6 18a4 4 0 0 1-2-7.464M6.003 5.125a4 4 0 0 0-2.526 5.77"},
	{"zap", "M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z"},
	{"star", "M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z"},
	{"heart", "M2 9.5a5.5 5.5 0 0 1 9.591-3.676.56.56 0 0 0 .818 0A5.49 5.49 0 0 1 22 9.5c0 2.29-1.5 4-3 5.5l-5.492 5.313a2 2 0 0 1-3 .019L5 15c-1.5-1.5-3-3.2-3-5.5"},
	{"book-open", "M12 7v14M3 18a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h5a4 4 0 0 1 4 4 4 4 0 0 1 4-4h5a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1h-6a3 3 0 0 0-3 3 3 3 0 0 0-3-3z"},
	{"briefcase", "M16 20V4a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v16M4 6h16a2 2 0 0 1 2 2v10a2 2 0 0 1 -2 2h-16a2 2 0 0 1 -2 -2v-10a2 2 0 0 1 2 -2Z"},
	{"cloud", "M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"},
	{"gamepad-2", "M6 11L10 11M8 9L8 13M15 12L15.01 12M18 10L18.01 10M17.32 5H6.68a4 4 0 0 0-3.978 3.59c-.006.052-.01.101-.017.152C2.604 9.416 2 14.456 2 16a3 3 0 0 0 3 3c1 0 1.5-.5 2-1l1.414-1.414A2 2 0 0 1 9.828 16h4.344a2 2 0 0 1 1.414.586L17 18c.5.5 1 1 2 1a3 3 0 0 0 3-3c0-1.545-.604-6.584-.685-7.258-.007-.05-.011-.1-.017-.151A4 4 0 0 0 17.32 5z"},
	{"music", "M9 18V5l12-2v13M3 18a3 3 0 1 0 6 0a3 3 0 1 0 -6 0M15 16a3 3 0 1 0 6 0a3 3 0 1 0 -6 0"},
	{"camera", "M13.997 4a2 2 0 0 1 1.76 1.05l.486.9A2 2 0 0 0 18.003 7H20a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2h1.997a2 2 0 0 0 1.759-1.048l.489-.904A2 2 0 0 1 10.004 4zM9 13a3 3 0 1 0 6 0a3 3 0 1 0 -6 0"},
	{"palette", "M12 22a1 1 0 0 1 0-20 10 9 0 0 1 10 9 5 5 0 0 1-5 5h-2.25a1.75 1.75 0 0 0-1.4 2.8l.3.4a1.75 1.75 0 0 1-1.4 2.8zM13 6.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0M17 10.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0M6 12.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0M8 7.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0"},
	{"shield", "M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"},
	{"wrench", "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.106-3.105c.32-.322.863-.22.983.218a6 6 0 0 1-8.259 7.057l-7.91 7.91a1 1 0 0 1-2.999-3l7.91-7.91a6 6 0 0 1 7.057-8.259c.438.12.54.662.219.984z"},
	{"hammer", "M0 0m15 12-9.373 9.373a1 1 0 0 1-3.001-3L12 9M0 0m18 15 4-4M0 0m21.5 11.5-1.914-1.914A2 2 0 0 1 19 8.172v-.344a2 2 0 0 0-.586-1.414l-1.657-1.657A6 6 0 0 0 12.516 3H9l1.243 1.243A6 6 0 0 1 12 8.485V10l2 2h1.172a2 2 0 0 1 1.414.586L18.5 14.5"},
	{"flask-conical", "M14 2v6a2 2 0 0 0 .245.96l5.51 10.08A2 2 0 0 1 18 22H6a2 2 0 0 1-1.755-2.96l5.51-10.08A2 2 0 0 0 10 8V2M6.453 15h11.094M8.5 2h7"},
	{"leaf", "M11 20A7 7 0 0 1 9.8 6.1C15.5 5 17 4.48 19 2c1 2 2 4.18 2 8 0 5.5-4.78 10-10 10ZM2 21c0-3 1.85-5.36 5.08-6C9.5 14.52 12 13 13 12"},
	{"coffee", "M10 2v2M14 2v2M16 8a1 1 0 0 1 1 1v8a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4V9a1 1 0 0 1 1-1h14a4 4 0 1 1 0 8h-1M6 2v2"},
	{"house", "M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8M3 10a2 2 0 0 1 .709-1.528l7-6a2 2 0 0 1 2.582 0l7 6A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"},
	{"graduation-cap", "M21.42 10.922a1 1 0 0 0-.019-1.838L12.83 5.18a2 2 0 0 0-1.66 0L2.6 9.08a1 1 0 0 0 0 1.832l8.57 3.908a2 2 0 0 0 1.66 0zM22 10v6M6 12.5V16a6 3 0 0 0 12 0v-3.5"},
	{"puzzle", "M15.39 4.39a1 1 0 0 0 1.68-.474 2.5 2.5 0 1 1 3.014 3.015 1 1 0 0 0-.474 1.68l1.683 1.682a2.414 2.414 0 0 1 0 3.414L19.61 15.39a1 1 0 0 1-1.68-.474 2.5 2.5 0 1 0-3.014 3.015 1 1 0 0 1 .474 1.68l-1.683 1.682a2.414 2.414 0 0 1-3.414 0L8.61 19.61a1 1 0 0 0-1.68.474 2.5 2.5 0 1 1-3.014-3.015 1 1 0 0 0 .474-1.68l-1.683-1.682a2.414 2.414 0 0 1 0-3.414L4.39 8.61a1 1 0 0 1 1.68.474 2.5 2.5 0 1 0 3.014-3.015 1 1 0 0 1-.474-1.68l1.683-1.682a2.414 2.414 0 0 1 3.414 0z"},
	{"git-branch", "M15 6a9 9 0 0 0-9 9V3M15 6a3 3 0 1 0 6 0a3 3 0 1 0 -6 0M3 18a3 3 0 1 0 6 0a3 3 0 1 0 -6 0"},
	{"smartphone", "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1 -2 2h-10a2 2 0 0 1 -2 -2v-16a2 2 0 0 1 2 -2ZM12 18h.01"},
	{"monitor", "M4 3h16a2 2 0 0 1 2 2v10a2 2 0 0 1 -2 2h-16a2 2 0 0 1 -2 -2v-10a2 2 0 0 1 2 -2ZM8 21L16 21M12 17L12 21"},
	{"atom", "M11 12a1 1 0 1 0 2 0a1 1 0 1 0 -2 0M20.2 20.2c2.04-2.03.02-7.36-4.5-11.9-4.54-4.52-9.87-6.54-11.9-4.5-2.04 2.03-.02 7.36 4.5 11.9 4.54 4.52 9.87 6.54 11.9 4.5ZM15.7 15.7c4.52-4.54 6.54-9.87 4.5-11.9-2.03-2.04-7.36-.02-11.9 4.5-4.52 4.54-6.54 9.87-4.5 11.9 2.03 2.04 7.36.02 11.9-4.5Z"},
}

// projectIcon is the path data for a project's lucide icon name. Names the
// popover doesn't offer fall back to "folder", as aide does for unknown ones.
func projectIcon(name string) string {
	for _, ic := range projectIcons {
		if ic.name == name {
			return ic.d
		}
	}
	return icFolder
}
