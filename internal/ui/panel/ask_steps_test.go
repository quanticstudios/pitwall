package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func threeQuestions() []session.Question {
	return []session.Question{
		{
			ID: "color", Header: "Color", Prompt: "Which accent color should we use?",
			Options: []session.PromptOption{
				{ID: "blue", Label: "Ocean Blue (Recommended)", Description: "Matches the active sidebar pill"},
				{ID: "green", Label: "Mint Green", Description: "Calm contrast on dark surfaces"},
				{ID: "amber", Label: "Warm Amber", Description: "High visibility for warnings"},
			},
		},
		{
			ID: "features", Header: "Features", Prompt: "Which extras should be enabled?", Multi: true,
			Options: []session.PromptOption{
				{ID: "keys", Label: "Keyboard shortcuts", Description: "Number keys 1-9 and arrow navigation"},
				{ID: "preview", Label: "Live preview", Description: "Render option previews beside the list"},
				{ID: "sync", Label: "TUI sync", Description: "Follow answers made directly in the terminal"},
			},
		},
		{
			ID: "scope", Header: "Scope", Prompt: "Where should this setting apply?",
			Options: []session.PromptOption{
				{ID: "tab", Label: "This tab only", Description: "Resets when the tab closes"},
				{ID: "workspace", Label: "Workspace default", Description: "Saved for every tab in this repo"},
			},
		},
	}
}

// TestAskStepsNavigation verifies step-by-step navigation on a 3-question
// card: single-select advances at once, multi-select toggles checkboxes
// and advances on Next/Enter, Back (← and chips) keeps earlier answers,
// and the last question sends all answers in the protocol shape.
func TestAskStepsNavigation(t *testing.T) {
	d := newDesk(t, askSnap(threeQuestions()...))
	a := d.tr.asks["i1"]
	if a == nil || a.step != 0 {
		t.Fatalf("initial ask state %+v, want step 0", a)
	}

	// Step 1: pick option 2 ("Mint Green") by number key -> moves to step 2.
	if !d.tr.Key("2") {
		t.Fatal("key 2 did nothing on step 1")
	}
	d.frame()
	if a.step != 1 || len(d.tr.Inputs()) != 0 {
		t.Fatalf("after step 1 pick: step %d, inputs %+v", a.step, d.tr.Inputs())
	}

	// Step 2 (multi-select): pick 1 and 3, stay on step 2 until Next / Enter.
	d.tr.Key("1")
	d.tr.Key("3")
	d.frame()
	if a.step != 1 || !a.multi[1][0] || a.multi[1][1] || !a.multi[1][2] {
		t.Fatalf("after multi toggles: step %d, multi %v", a.step, a.multi[1])
	}
	a.next.Click()
	d.frame()
	if a.step != 2 || len(d.tr.Inputs()) != 0 {
		t.Fatalf("after Next: step %d, inputs %+v", a.step, d.tr.Inputs())
	}

	// Back via Left arrow returns to step 2 with checkboxes kept.
	d.r.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	d.frame()
	if a.step != 1 || !a.multi[1][0] || !a.multi[1][2] {
		t.Fatalf("after Left arrow: step %d, multi %v", a.step, a.multi[1])
	}

	// Click chip 0 to jump back to step 1; Mint Green is still selected.
	a.chips[0].Click()
	d.frame()
	if a.step != 0 || a.pick[0] != 1 {
		t.Fatalf("after chip 0: step %d, pick[0] %d", a.step, a.pick[0])
	}

	// Jump to step 3 via chip 2, type Other in composer -> sends everything.
	a.chips[2].Click()
	d.frame()
	if a.step != 2 {
		t.Fatalf("after chip 2: step %d, want 2", a.step)
	}
	d.typ("Global config")
	d.r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	d.frame()
	got := d.tr.Inputs()
	want := map[string][]string{
		"color":    {"Mint Green"},
		"features": {"Keyboard shortcuts", "TUI sync"},
		"scope":    {"Global config"},
	}
	if len(got) != 1 || got[0].Step != "" || !reflect.DeepEqual(got[0].Answers, want) {
		t.Fatalf("final inputs %+v, want %v", got, want)
	}
}

// TestAskLiveTUISyncAndReview verifies live TUI step-by-step answering,
// Claude Code's mirrored Review/Submit step, direct TUI answering moving
// the card along, step refusal recovery, and withdrawal closing the card.
func TestAskLiveTUISyncAndReview(t *testing.T) {
	step0 := 0
	snap := askSnap(threeQuestions()...)
	snap.PendingUserInputs[0].Step = &step0
	snap.PendingUserInputs[0].Review = true
	snap.PendingUserInputs[0].Done = []bool{false, false, false}

	f := &askFrames{s: snap}
	f.tr.LiveSteps = true
	f.frame()
	a := f.tr.asks["i1"]
	if !a.hasTUI || !a.review || a.numSteps() != 4 || a.step != 0 {
		t.Fatalf("live ask %+v", a)
	}

	// Step 1 single-select emits a live "answer" step and advances optimistically.
	f.tr.Key("1")
	got := f.tr.Inputs()
	if len(got) != 1 || got[0].Step != "answer" || got[0].Target != 0 || !reflect.DeepEqual(got[0].Answers, map[string][]string{"color": {"Ocean Blue (Recommended)"}}) {
		t.Fatalf("step 1 live input: %+v", got)
	}
	if a.step != 1 || f.tr.sent["i1"] {
		t.Fatalf("after step 1: step %d, sent %v", a.step, f.tr.sent["i1"])
	}

	// Simulate TUI confirming step 1 -> step 2.
	step1 := 1
	snap.PendingUserInputs[0].Step = &step1
	snap.PendingUserInputs[0].Done = []bool{true, false, false}
	snap.PendingUserInputs[0].Answered = map[string][]string{"color": {"Ocean Blue (Recommended)"}}
	f.frame()
	if a.step != 1 || !a.stepDone(snap.PendingUserInputs[0].Questions, 0) {
		t.Fatalf("after TUI step 1: step %d, done %v", a.step, a.done)
	}

	// Simulate the user answering step 2 directly in the TUI -> card moves to step 3!
	step2 := 2
	snap.PendingUserInputs[0].Step = &step2
	snap.PendingUserInputs[0].Done = []bool{true, true, false}
	snap.PendingUserInputs[0].Answered["features"] = []string{"Live preview"}
	f.frame()
	if a.step != 2 || !a.multi[1][1] {
		t.Fatalf("TUI direct answer did not sync card: step %d, multi %v", a.step, a.multi[1])
	}

	// Back arrow emits live "goto" step to target 1.
	f.tr.Key(string(key.NameLeftArrow))
	got = f.tr.Inputs()
	if len(got) != 1 || got[0].Step != "goto" || got[0].Target != 1 {
		t.Fatalf("goto input %+v", got)
	}

	// Simulate daemon refusing the step -> card snaps back to TUI's step 2 with note.
	f.tr.StepError("i1", "Claude Code is on another question")
	if a.step != 2 || a.note != "Claude Code is on another question" {
		t.Fatalf("after StepError: step %d, note %q", a.step, a.note)
	}

	// Answer step 3 -> moves to Review step (3), not sent yet!
	f.tr.Key("2")
	got = f.tr.Inputs()
	if len(got) != 1 || got[0].Step != "answer" || got[0].Target != 2 {
		t.Fatalf("step 3 live input: %+v", got)
	}
	if !a.onReview() || f.tr.sent["i1"] {
		t.Fatalf("after step 3: step %d, onReview %v, sent %v", a.step, a.onReview(), f.tr.sent["i1"])
	}

	// TUI reaches Review step (3), clicking row 0 jumps back to edit step 0.
	step3 := 3
	snap.PendingUserInputs[0].Step = &step3
	snap.PendingUserInputs[0].Done = []bool{true, true, true}
	snap.PendingUserInputs[0].Answered["scope"] = []string{"Workspace default"}
	f.frame()
	a.revRows[0].Click()
	f.frame()
	if a.step != 0 {
		t.Fatalf("review row click: step %d, want 0", a.step)
	}
	if got := f.tr.Inputs(); len(got) != 1 || got[0].Step != "goto" || got[0].Target != 0 {
		t.Fatalf("review row goto %+v", got)
	}

	// Jump to Submit chip (3) and press Enter -> emits "submit" and marks sent.
	a.chips[3].Click()
	f.frame()
	_ = f.tr.Inputs()
	if !f.tr.Key(string(key.NameReturn)) {
		t.Fatal("Enter on Review did not submit")
	}
	got = f.tr.Inputs()
	if len(got) != 1 || got[0].Step != "submit" || got[0].Target != 3 || !f.tr.sent["i1"] {
		t.Fatalf("submit input %+v, sent %v", got, f.tr.sent["i1"])
	}

	// Agent withdraws a pending question -> card closes cleanly.
	snap.PendingUserInputs = []session.InputRequest{{ID: "i2", Questions: threeQuestions()}}
	f.frame()
	if c, _ := f.cards(); c != 1 {
		t.Fatalf("before withdrawal: %d cards", c)
	}
	snap.PendingUserInputs = nil
	f.frame()
	if c, _ := f.cards(); c != 0 || len(f.tr.asks) != 0 {
		t.Fatalf("after withdrawal: %d cards, %d asks", c, len(f.tr.asks))
	}
}

// TestAskStepsShots renders the question card variants headless (at 1.5x
// scale) in both Column and Ledger chat layouts to PITWALL_ASK_SHOTS for
// visual inspection and PR screenshots.
func TestAskStepsShots(t *testing.T) {
	dir := os.Getenv("PITWALL_ASK_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_ASK_SHOTS to render question card PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Builtin("aide-dark")
	th, err := theme.New(cfg, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	const scale = 1.5
	w, h := int(960*scale), int(640*scale)
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()

	renderLayout := func(chatLayout, prefix string) {
		render := func(name string, snap *session.Snapshot, prep func(*Transcript)) {
			t.Helper()
			var tr Transcript
			var cmp chatin.Composer
			tr.Width = 760
			tr.ChatLayout = chatLayout
			ledger := chatLayout == LayoutLedger
			var ops op.Ops
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			draw := func() {
				ops.Reset()
				now = now.Add(16 * time.Millisecond)
				gtx := layout.Context{
					Ops:         &ops,
					Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
					Constraints: layout.Exact(image.Pt(w, h)),
					Now:         now,
				}
				cw := unit.Dp(760)
				if ledger {
					cw = LedgerWidth(snap.Provider)
				}
				env := chatin.Env{Chat: "c", Snap: snap, Width: cw, Ledger: ledger, Focus: false, Key: tr.Key, Answer: tr.Answer}
				layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return tr.Layout(gtx, th, snap, false) }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cmp.Layout(gtx, th, env) }),
				)
				if err := win.Frame(&ops); err != nil {
					t.Fatal(err)
				}
			}
			draw()
			if prep != nil {
				prep(&tr)
				draw()
			}
			draw()
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			if err := win.Screenshot(img); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(dir, prefix+name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
		}

		// 1. Single-question card
		render("ask-single.png", askSnap(session.Question{
			ID: "layout", Header: "Layout", Prompt: "Where should the settings panel live?",
			Options: []session.PromptOption{
				{ID: "side", Label: "Sidebar (Recommended)", Description: "Collapsible panel beside the chat"},
				{ID: "modal", Label: "Modal dialog", Description: "Centered overlay opened from the header"},
				{ID: "tab", Label: "Dedicated tab", Description: "Opens as a regular workspace tab"},
			},
		}), nil)

		// 2. Three-question card on step 1
		s3 := askSnap(threeQuestions()...)
		s3.PendingUserInputs[0].Review = true
		render("ask-3q-step1.png", s3, nil)

		// 3. Three-question card on step 2 (multi-select with 2 checked)
		render("ask-3q-step2-multi.png", s3, func(tr *Transcript) {
			tr.Key("1") // Ocean Blue -> advances to step 2
			tr.Key("1") // check Keyboard shortcuts
			tr.Key("3") // check TUI sync
		})

		// 4. Three-question card on step 3 (last question, step 1 & 2 checked)
		render("ask-3q-step3-last.png", s3, func(tr *Transcript) {
			tr.Key("1")
			tr.Key("1")
			tr.Key("3")
			tr.Key(string(key.NameReturn)) // advance to step 3
		})

		// 5. Three-question card on Review/Submit step
		render("ask-3q-review.png", s3, func(tr *Transcript) {
			tr.Key("1")
			tr.Key("1")
			tr.Key("3")
			tr.Key(string(key.NameReturn))
			tr.Key("2") // Workspace default -> Review step
		})

		// 6. Standalone multi-select question card
		render("ask-multi.png", askSnap(session.Question{
			ID: "checks", Header: "Checks", Prompt: "Which verification steps should run before pushing?", Multi: true,
			Options: []session.PromptOption{
				{ID: "fmt", Label: "gofmt", Description: "Format Go sources"},
				{ID: "vet", Label: "go vet", Description: "Static analysis across all packages"},
				{ID: "race", Label: "go test -race", Description: "Run unit tests with the race detector"},
			},
		}), func(tr *Transcript) {
			tr.Key("1")
			tr.Key("2")
		})

		// 7. Question card with side-by-side option previews
		render("ask-preview.png", askSnap(session.Question{
			ID: "layout", Header: "Layout", Prompt: "Which terminal split layout do you prefer?",
			Options: []session.PromptOption{
				{
					ID: "split", Label: "Side-by-side (Recommended)", Description: "Chat on left, terminal on right",
					Preview: "┌──────────────┬──────────────┐\n│              │ $ go test    │\n│  Chat panel  │ PASS         │\n│              │ ok  0.42s    │\n└──────────────┴──────────────┘",
				},
				{
					ID: "stack", Label: "Stacked", Description: "Chat on top, terminal drawer below",
					Preview: "┌─────────────────────────────┐\n│         Chat panel          │\n├─────────────────────────────┤\n│ $ go test  PASS 0.42s       │\n└─────────────────────────────┘",
				},
			},
		}), nil)
	}

	renderLayout(LayoutColumn, "column-")
	renderLayout(LayoutLedger, "ledger-")
}
