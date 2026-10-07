package app

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// clockWindow is keyWindow with a clock the test moves: send queues key
// events and draws a frame at now.
func clockWindow(keys *config.Bindings) (u *ui, now *time.Time, send func(...key.Event)) {
	u = &ui{b: NewFakeBackend(), th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: keys}}
	var r input.Router
	var ops op.Ops
	t := time.Now()
	send = func(es ...key.Event) {
		for _, e := range es {
			r.Queue(e)
		}
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: t}
		u.layout(gtx)
		r.Frame(&ops)
	}
	send()
	send()
	return u, &t, send
}

func release(n key.Name, m key.Modifiers) key.Event {
	return key.Event{Name: n, Modifiers: m, State: key.Release}
}

// TestGotoVisibleRows: Alt+N counts the rows the sidebar shows. The fake
// session starts on ungrouped w1 with groups g1 (w4, w5) and g2 (w6)
// collapsed; going into g1 expands it, as the sidebar does.
func TestGotoVisibleRows(t *testing.T) {
	u, _, send := clockWindow(conventional)
	alt := key.ModAlt
	send(press("3", alt), release("3", alt))
	if u.nav.workspace != "w1c" {
		t.Fatalf("Alt+3: at %s, want w1c", u.nav.workspace)
	}
	send(press("6", alt), release("6", alt))
	if u.nav.workspace != "w1c" {
		t.Fatalf("Alt+6 with every group collapsed: at %s, want w1c", u.nav.workspace)
	}
	st := u.b.State()
	u.nav.selectWorkspace(&st, "w4", "")
	send(press("7", alt), release("7", alt))
	if u.nav.workspace != "w5" {
		t.Fatalf("Alt+7 with g1 open: at %s, want w5", u.nav.workspace)
	}
	send(press("8", alt), release("8", alt))
	if u.nav.workspace != "w5" {
		t.Fatalf("Alt+8 into collapsed g2: at %s, want w5", u.nav.workspace)
	}
}

// TestGotoDigits: the digits show once Alt has been down alone for
// hintDelay, and go on its release, on a chord pressed within the delay,
// and when the window loses focus.
func TestGotoDigits(t *testing.T) {
	u, now, send := clockWindow(conventional)
	alt := key.ModAlt
	shown := func() bool { return u.sidebar.Numbers[0] && u.sidebar.Numbers[8] }
	step := func(d time.Duration) { *now = now.Add(d); send() }

	send(press(key.NameAlt, 0))
	if shown() {
		t.Fatal("digits showed before the delay")
	}
	step(hintDelay)
	if !shown() {
		t.Fatal("Alt held: no digits")
	}
	send(release(key.NameAlt, alt))
	if shown() {
		t.Fatal("Alt released: digits stayed")
	}

	send(press(key.NameAlt, 0))
	step(hintDelay / 4)
	send(press("2", alt), release("2", alt))
	step(hintDelay)
	if shown() || u.nav.workspace != "w1b" {
		t.Fatalf("quick Alt+2: digits %v, at %s", shown(), u.nav.workspace)
	}
	send(release(key.NameAlt, alt))

	send(press(key.NameAlt, 0))
	step(hintDelay)
	u.blur()
	send()
	if shown() {
		t.Fatal("focus lost: digits stayed")
	}
}

// TestGotoDigitsCtrl: goto_tab rebound to Ctrl+N shows the digits on Ctrl,
// not Alt; bindings that disagree or are unbound show none.
func TestGotoDigitsCtrl(t *testing.T) {
	load := func(gotos func(i int) string) *config.Bindings {
		t.Helper()
		toml := "[keys]\n"
		for i := 1; i <= 9; i++ {
			toml += fmt.Sprintf("goto_tab_%d = %s\n", i, gotos(i))
		}
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		s, probs := config.LoadFile(p)
		if len(probs) > 0 {
			t.Fatal(probs)
		}
		return s.Keys
	}
	ctrl := load(func(i int) string { return fmt.Sprintf(`["Ctrl+%d"]`, i) })
	u, now, send := clockWindow(ctrl)
	send(press(key.NameAlt, 0))
	*now = now.Add(hintDelay)
	send()
	if u.sidebar.Numbers[0] {
		t.Fatal("Alt showed digits for Ctrl bindings")
	}
	send(release(key.NameAlt, key.ModAlt), press(key.NameCtrl, 0))
	*now = now.Add(hintDelay)
	send()
	if !u.sidebar.Numbers[0] {
		t.Fatal("Ctrl held: no digits")
	}

	for name, b := range map[string]*config.Bindings{
		"mixed": load(func(i int) string {
			if i == 1 {
				return `["Ctrl+1"]`
			}
			return fmt.Sprintf(`["Alt+%d"]`, i)
		}),
		"unbound": load(func(int) string { return "[]" }),
	} {
		if mods, _ := gotoKeys(b); mods != 0 {
			t.Errorf("%s: goto modifiers %v, want none", name, mods)
		}
	}
	if _, bound := gotoKeys(load(func(i int) string {
		if i == 9 {
			return "[]"
		}
		return fmt.Sprintf(`["Alt+%d"]`, i)
	})); !bound[7] || bound[8] {
		t.Errorf("Alt+9 unbound: digits %v, want 1-8", bound)
	}
}
