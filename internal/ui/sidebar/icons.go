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
	icArchive     = "M3 3h18a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1ZM4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8M10 12h4"
	icFolderKanb  = "M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2ZM8 10v4M12 10v2M16 10v6"
	icMessage     = "M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z"
	icPencil      = "M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497zM15 5l4 4"
	icTrash       = "M10 11v6M14 11v6M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
	icSquareTerm  = "m7 11 2-2-2-2M11 13h4M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z"
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
