package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// lucide triangle-alert
const icTriangleAlert = "M21.73 18l-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3ZM12 9v4M12 17h.01"

// radarFiles is how many of one overlap's files a hover card lists.
const radarFiles = 4

// radarColor is the mark of a tab with overlaps: red when a merge would
// conflict, yellow when they only share files. ok is false with none.
func radarColor(th *theme.Theme, overlaps []model.Overlap) (c color.NRGBA, ok bool) {
	if len(overlaps) == 0 {
		return c, false
	}
	for _, o := range overlaps {
		if o.Conflicts > 0 {
			return th.Red, true
		}
	}
	return th.Yellow, true
}

// radarMark is the warning mark on line 2 of tab id's row, after its diff
// stats, when its branch shares files with another tab's.
func radarMark(v *view, id string) []item {
	col, ok := radarColor(v.th, v.st.Overlaps[id])
	if !ok {
		return nil
	}
	return []item{{w: func(gtx layout.Context) layout.Dimensions {
		return drawIcon(gtx, icTriangleAlert, gtx.Dp(11), col, 0)
	}}}
}

// radarEntry is one other tab in a hover card's conflict radar.
type radarEntry struct {
	name string // the other tab's title, else its branch
	o    model.Overlap
	// first is the tab to merge first, "this tab" or name; "" when
	// nothing tells the two apart.
	first string
}

// radarEntries is tab ws's overlaps for its hover card.
func radarEntries(v *view, ws model.Workspace) []radarEntry {
	var out []radarEntry
	for _, o := range v.st.Overlaps[ws.ID] {
		e := radarEntry{name: o.Branch, o: o}
		for _, w := range v.st.Workspaces {
			if w.ID == o.WorkspaceID {
				e.name = Title(w)
			}
		}
		switch mergeFirst(v.st, ws.ID, o.WorkspaceID) {
		case -1:
			e.first = "this tab"
		case 1:
			e.first = e.name
		}
		out = append(out, e)
	}
	return out
}

// mergeFirst says which of tabs a and b to merge first, -1 for a and 1
// for b: the one whose branch conflicts in fewer files with every other
// tab's, else the one not behind its default branch, else the one with
// fewer changed lines. 0 when nothing tells them apart.
func mergeFirst(st *model.State, a, b string) int {
	conflicts := func(id string) (n int) {
		for _, o := range st.Overlaps[id] {
			n += o.Conflicts
		}
		return n
	}
	behind := func(id string) int { return min(st.Stats[id].Behind, 1) }
	lines := func(id string) int { return st.Stats[id].Additions + st.Stats[id].Deletions }
	for _, f := range []func(string) int{conflicts, behind, lines} {
		if x, y := f(a), f(b); x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// radarBody is one radar entry of a hover card: the other tab and the
// merge order, then the shared files, conflicting ones in red, each a
// button when btn gives one.
func radarBody(gtx layout.Context, th *theme.Theme, bg color.NRGBA, e radarEntry, btn func(other, path string) *widget.Clickable) layout.Dimensions {
	muted := theme.Mix(bg, th.Muted, 0.9)
	head, col := "Same files as "+e.name, th.Yellow
	if e.o.Conflicts > 0 {
		head, col = "Conflicts with "+e.name, th.Red
	}
	rows := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = gtx.Sp(13 * 1.5) // level with the icon
		return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), 12, col, head)
		})
	})}
	if e.first != "" {
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, 11, muted, "Merge "+e.first+" first")
		}))
	}
	for i, f := range e.o.Files {
		if i == radarFiles {
			rest := len(e.o.Files) - i
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, 11, muted, fmt.Sprintf("%d more %s", rest, plural(rest, "file", "files")))
			}))
			break
		}
		fc := theme.Mix(bg, th.Fg, 0.85)
		if i < e.o.Conflicts {
			fc = th.Red
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			draw := func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return layout.Inset{Top: 1, Bottom: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.MonoFont, 11, fc, f)
				})
			}
			if btn == nil {
				return draw(gtx)
			}
			c := btn(e.o.WorkspaceID, f)
			return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
				if c.Hovered() {
					fc = theme.Mix(fc, th.Fg, 0.5)
				}
				return draw(gtx)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// radarInput reads the card's pointer and its file buttons before a frame
// draws it: the pointer on a card with a radar keeps it open, and a click
// on a file asks for its diff in the card's tab and closes the card.
func (s *Sidebar) radarInput(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.cardTag, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			s.cardHover = e.Kind == pointer.Enter
		}
	}
	if s.hover.shown == "" {
		s.cardHover = false
	}
	if s.fileFor != s.hover.shown {
		s.fileBtn, s.fileFor = nil, s.hover.shown
	}
	for k, c := range s.fileBtn {
		if c.Clicked(gtx) {
			_, path, _ := strings.Cut(k, "\x00")
			s.events = append(s.events, ViewFileDiff{WorkspaceID: s.fileFor, Path: path})
			s.hover.dismiss()
			s.cardHover = false
		}
	}
}

// fileButton is the button of path, shared with tab other, in the open
// card's radar.
func (s *Sidebar) fileButton(other, path string) *widget.Clickable {
	if s.fileBtn == nil {
		s.fileBtn = map[string]*widget.Clickable{}
	}
	k := other + "\x00" + path
	c := s.fileBtn[k]
	if c == nil {
		c = &widget.Clickable{}
		s.fileBtn[k] = c
	}
	return c
}
