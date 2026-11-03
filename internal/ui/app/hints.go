package app

import (
	"image"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gioui.org/io/key"
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/term"
)

// hintMode is hints mode: a label on each match in the focused pane, or in
// every pane the tab shows, until a label is typed or Esc.
type hintMode struct {
	on, all  bool
	ws, pane string // the tab and the focused pane it opened on
	panes    []string
	typed    string // the label letters typed so far
	open     bool   // Shift was held on a typed letter: open, not copy
	// labels keeps a match's label while the match shows, by labelKey.
	labels  map[string]string
	targets map[string]hintTarget // what each label shown copies and opens
}

// hintTarget is a labelled match and the pane it shows in.
type hintTarget struct {
	pane string
	term.HintMatch
}

// openHints turns hints mode on for the focused pane, or with all for
// every pane the tab shows.
func (u *ui) openHints(all bool) {
	id := u.nav.focused()
	if id == "" {
		return
	}
	u.paneMenu.close()
	u.hints = hintMode{on: true, all: all, ws: u.nav.workspace, pane: id}
}

func (u *ui) closeHints() { u.hints = hintMode{} }

// hintKey runs one key in hints mode and reports whether it used it. A
// window chord other than the hints keys leaves the mode and runs.
func (u *ui) hintKey(gtx gl.Context, st *model.State, e key.Event) bool {
	h := &u.hints
	if e.State != key.Press {
		return true
	}
	a := u.nav.bind().Action(e)
	switch {
	case e.Name == key.NameEscape, a == "hints" && !h.all, a == "hints_all" && h.all:
		u.closeHints()
	case a == "hints", a == "hints_all":
		h.all, h.typed, h.open = a == "hints_all", "", false
	case e.Name == key.NameDeleteBackward:
		h.typed = h.typed[:max(0, len(h.typed)-1)]
	case len(e.Name) == 1 && e.Name[0] >= 'A' && e.Name[0] <= 'Z' && e.Modifiers&^key.ModShift == 0:
		typed := h.typed + strings.ToLower(string(e.Name))
		if !startsLabel(h.targets, typed) {
			break // a letter that starts no label does nothing
		}
		h.typed, h.open = typed, h.open || e.Modifiers&key.ModShift != 0
		if t, ok := h.targets[typed]; ok {
			open := h.open
			u.closeHints()
			u.hintAct(gtx, st, t, open)
		}
	case a != "" && !modifierKey(e.Name):
		u.closeHints()
		return false
	}
	if !modifierKey(e.Name) {
		u.nav.swallow = e.Name // its release must not reach the pane
	}
	return true
}

// startsLabel reports whether typed starts one of the labels shown.
func startsLabel(shown map[string]hintTarget, typed string) bool {
	for l := range shown {
		if strings.HasPrefix(l, typed) {
			return true
		}
	}
	return false
}

// layoutHints scans the panes hints mode labels, of order the tab's
// panes, and returns each one's hints with their labels. It leaves the mode
// when its tab or focused pane is no longer the one shown.
func (u *ui) layoutHints(st *model.State, ws string, order []string) map[string][]term.Hint {
	h := &u.hints
	if !h.on {
		return nil
	}
	if ws != h.ws || u.nav.focused() != h.pane || !slices.Contains(order, h.pane) {
		u.closeHints()
		return nil
	}
	h.panes = []string{h.pane}
	if h.all {
		for _, id := range order {
			if id != h.pane {
				h.panes = append(h.panes, id)
			}
		}
	}
	type hit struct {
		pane, key string
		m         term.HintMatch
	}
	var hits []hit
	var keys []string
	for _, id := range h.panes {
		g, _, ok := u.b.Frame(id)
		if !ok {
			continue
		}
		ms := term.ScanHints(&g, u.cfg.HintPatterns)
		for i := len(ms) - 1; i >= 0; i-- { // the newest output, at the bottom, gets the shortest labels
			k := labelKey(id, ms[i])
			hits = append(hits, hit{id, k, ms[i]})
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	h.labels = assignHintLabels(keys, h.labels)
	if !slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(h.labels[k], h.typed) }) {
		h.typed = "" // the screen changed under the typed letters
	}
	h.targets = map[string]hintTarget{}
	out := map[string][]term.Hint{}
	for _, x := range hits {
		l := h.labels[x.key]
		if !strings.HasPrefix(l, h.typed) {
			continue
		}
		out[x.pane] = append(out[x.pane], term.Hint{HintMatch: x.m, Label: l, Typed: len(h.typed)})
		h.targets[l] = hintTarget{x.pane, x.m}
	}
	return out
}

// labelKey is what gives a match its label: the same text has the same
// label in every pane, except a path, which each pane reads from its own
// folder.
func labelKey(pane string, m term.HintMatch) string {
	if m.Kind == term.HintPath {
		return pane + "\x00" + m.Text
	}
	return m.Text
}

// assignHintLabels gives each key, in order, a label: the one it had in
// prev while that label is still in the set for this many keys, and else
// the first one left. So labels stay put while the screen does.
func assignHintLabels(keys []string, prev map[string]string) map[string]string {
	set := term.HintLabels(len(keys))
	out := make(map[string]string, len(keys))
	used := map[string]bool{}
	for _, k := range keys {
		if l, ok := prev[k]; ok && slices.Contains(set, l) && !used[l] {
			out[k], used[l] = l, true
		}
	}
	next := 0
	for _, k := range keys {
		if _, ok := out[k]; ok {
			continue
		}
		for used[set[next]] {
			next++
		}
		out[k], used[set[next]] = set[next], true
	}
	return out
}

// hintAct copies t's text or, with open, opens it, copying what cannot
// open.
func (u *ui) hintAct(gtx gl.Context, st *model.State, t hintTarget, open bool) {
	var r image.Rectangle
	if p := u.panes[t.pane]; p != nil {
		r = p.rect.Sub(u.contentOrigin)
	}
	target := ""
	if open {
		target = u.hintURL(st, t)
	}
	if target == "" {
		u.copyText(gtx, t.Text, r)
		return
	}
	if err := hintOpen(target); err != nil {
		log.Printf("hints: open %q: %v", target, err)
		u.copyText(gtx, t.Text, r)
		return
	}
	u.showNotice(gtx, "Opened "+t.Text, r)
}

// hintOpen opens a URL; tests replace it.
var hintOpen = OpenURL

var lineSuffix = regexp.MustCompile(`(:\d+)+$`)

// hintURL is what opening t opens: a URL in the browser, a path on this
// machine as a file URL, a #number or a commit on GitHub for the tab's
// repo. "" means t cannot open, and is copied.
func (u *ui) hintURL(st *model.State, t hintTarget) string {
	switch t.Kind {
	case term.HintURL, term.HintCustom:
		s := t.Text
		if strings.HasPrefix(strings.ToLower(s), "www.") {
			s = "https://" + s
		}
		if scheme, _, _ := strings.Cut(strings.ToLower(s), "://"); scheme == "http" || scheme == "https" {
			return s
		}
	case term.HintPath:
		cwd := ""
		if p := findPane(st, t.pane); p != nil {
			cwd = p.Cwd
		}
		if p := localPath(t.Text, cwd); p != "" {
			return fileURL(p)
		}
	case term.HintNumber:
		if repo := u.repoURL(st); repo != "" {
			return repo + "/pull/" + t.Text[1:]
		}
	case term.HintHash:
		if repo := u.repoURL(st); repo != "" {
			return repo + "/commit/" + t.Text
		}
	}
	return ""
}

// fileURL is the file URL of absolute path p, with forward slashes and a
// leading slash before a Windows drive: file:///C:/Users/x.
func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// localPath is path, without a :line or :line:col, as an absolute path
// on this machine, read from cwd when relative; "" when the file is not
// here, or the window is on a Host.
func localPath(path, cwd string) string {
	if curHost() != "" {
		return ""
	}
	p := lineSuffix.ReplaceAllString(path, "")
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		p = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return ""
		}
		p = filepath.Join(cwd, p)
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// repoURL is the GitHub page of the shown tab's repo: from its pull
// request, else from its origin remote; "" when neither says.
func (u *ui) repoURL(st *model.State) string {
	ws := findWorkspace(st, u.nav.workspace)
	if ws == nil {
		return ""
	}
	if repo, _, ok := strings.Cut(st.PRs[ws.ID].URL, "/pull/"); ok {
		return repo
	}
	if curHost() != "" {
		return "" // git would read this machine's checkout
	}
	out, err := exec.Command("git", "-C", st.LivePath(*ws), "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return githubURL(strings.TrimSpace(string(out)))
}

// githubURL is the web page of a github.com remote URL, https or ssh, or
// "" for another host.
func githubURL(remote string) string {
	r := strings.TrimSuffix(strings.TrimSuffix(remote, "/"), ".git")
	for _, p := range []string{"https://github.com/", "http://github.com/", "ssh://git@github.com/", "git@github.com:"} {
		if rest, ok := strings.CutPrefix(r, p); ok && strings.Count(rest, "/") == 1 {
			return "https://github.com/" + rest
		}
	}
	return ""
}
