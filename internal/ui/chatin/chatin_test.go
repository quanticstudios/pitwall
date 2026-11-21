package chatin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func pngOf(w, h int) []byte {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		im.Pix[i] = 0x80
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes()
}

// Image tokens: numbers go up within a draft and survive removals, a
// deleted token leaves its image (only its thumbnail's x takes it out),
// the images go in token order, those with no token last, and a cleared
// draft starts at #1 again.
func TestDraftTokens(t *testing.T) {
	var d Draft
	t1, t2, t3 := d.Add(Image{Mime: "image/png"}), d.Add(Image{Mime: "image/png"}), d.Add(Image{Mime: "image/png"})
	if t1 != "[Image #1]" || t2 != "[Image #2]" || t3 != "[Image #3]" {
		t.Fatalf("tokens %q %q %q", t1, t2, t3)
	}
	text := "see " + t3 + " then " + t1 + " and " + t2 + " ok"
	if got := numbers(d.Ordered(text)); !slices.Equal(got, []int{3, 1, 2}) {
		t.Fatalf("order %v, want 3 1 2", got)
	}
	text = strings.Replace(text, t1, "", 1) // the user deleted the token
	d.Sync(text)
	if got := numbers(d.Images); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("after deleting #1's token: %v", got)
	}
	if got := numbers(d.Ordered(text)); !slices.Equal(got, []int{3, 2, 1}) {
		t.Fatalf("order after deleting #1's token %v, want 3 2 1", got)
	}
	if Mentioned(text, 1) || !Mentioned(text, 2) {
		t.Fatal("Mentioned")
	}
	if tok := d.Add(Image{}); tok != "[Image #4]" {
		t.Fatalf("next token %q, want #4: numbers stay stable", tok)
	}
	if s, e := TokenSpan("ab [Image #2] cd", 2); s != 3 || e != 14 {
		t.Fatalf("span %d %d, want 3 14 with the space", s, e)
	}
	if s, _ := TokenSpan("ab", 2); s != -1 {
		t.Fatal("span of a missing token")
	}
	if got := d.TokenBefore("é [Image #2]x", 12); got != 2 {
		t.Fatalf("token before caret 12: %d", got)
	}
	if got := d.TokenBefore("é [Image #2]x", 13); got != -1 {
		t.Fatalf("token before caret 13: %d", got)
	}
	d.Clear()
	if tok := d.Add(Image{}); tok != "[Image #1]" {
		t.Fatalf("a cleared draft starts at %q", tok)
	}
	a := Attachments([]Image{{N: 2, Mime: "image/jpeg", Data: []byte("hi")}})
	if len(a) != 1 || a[0].ID != "image-2" || a[0].Kind != "image" || a[0].Name != "image-2.jpeg" || a[0].Data != base64.StdEncoding.EncodeToString([]byte("hi")) {
		t.Fatalf("attachment %+v", a)
	}
}

func numbers(ims []Image) []int {
	var n []int
	for _, im := range ims {
		n = append(n, im.N)
	}
	return n
}

// Shift+Tab's cycle: Default, Accept edits, Plan, and back; any other
// mode goes to Default. Plan shows from the interaction mode too.
func TestModes(t *testing.T) {
	for from, want := range map[string]string{ModeDefault: ModeAccept, ModeAccept: ModePlan, ModePlan: ModeDefault, ModeBypass: ModeDefault, "": ModeDefault} {
		if got := NextMode(from, false); got != want {
			t.Errorf("NextMode(%q) = %q, want %q", from, got, want)
		}
	}
	for from, want := range map[string]string{ModeDefault: ModeAccept, ModeAccept: ModePlan, ModePlan: ModeBypass, ModeBypass: ModeDefault} {
		if got := NextMode(from, true); got != want {
			t.Errorf("NextMode(%q) with bypass = %q, want %q", from, got, want)
		}
	}
	if m := ModeOf(&session.Snapshot{PermissionMode: "default", InteractionMode: "plan"}); m != ModePlan {
		t.Errorf("plan interaction shows %q", m)
	}
	if m := ModeOf(&session.Snapshot{}); m != ModeDefault {
		t.Errorf("no mode shows %q", m)
	}
}

// The meter: hidden without both numbers, a warning from 75%, danger near
// the auto-compact point, and its numbers to one decimal in k and M.
func TestMeter(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	if _, _, ok := Meter(&session.Usage{ContextTokens: n(5)}); ok {
		t.Error("a meter without the window size")
	}
	if _, _, ok := Meter(nil); ok {
		t.Error("a meter without usage")
	}
	if u, m, ok := Meter(&session.Usage{ContextTokens: n(62_000), MaxTokens: n(200_000)}); !ok || u != 62_000 || m != 200_000 {
		t.Errorf("meter %d %d %v", u, m, ok)
	}
	for _, c := range []struct {
		used, total int64
		pct         int
		level       MeterLevel
	}{
		{0, 200_000, 0, MeterOK},
		{40_000, 200_000, 20, MeterOK},
		{149_999, 200_000, 74, MeterOK},
		{150_000, 200_000, 75, MeterWarning},
		{173_999, 200_000, 86, MeterWarning},
		{174_000, 200_000, 87, MeterDanger},
		{199_999, 200_000, 99, MeterDanger},
		{210_000, 200_000, 100, MeterDanger},
		{840_000, 1_000_000, 84, MeterWarning},
		{5, 0, 0, MeterOK},
	} {
		if p, l := Percent(c.used, c.total), Level(c.used, c.total); p != c.pct || l != c.level {
			t.Errorf("%d of %d: %d%% level %d, want %d%% level %d", c.used, c.total, p, l, c.pct, c.level)
		}
	}
	if at := AutoCompactAt(200_000); at != 184_000 {
		t.Errorf("auto-compact at %d of 200k", at)
	}
	for v, want := range map[int64]string{950: "950", 62_400: "62.4k", 84_200: "84.2k", 200_000: "200k", 999_999: "1M", 1_000_000: "1M", 1_500_000: "1.5M"} {
		if got := Tokens(v); got != want {
			t.Errorf("Tokens(%d) = %q, want %q", v, got, want)
		}
	}
}

// The placeholder follows the chat, and the slash hook fires only while
// the caret is in a leading /word.
func TestPlaceholderAndSlash(t *testing.T) {
	if p := Placeholder(&session.Snapshot{SessionState: session.StateReady}); p != "Reply to Claude" {
		t.Errorf("idle: %q", p)
	}
	if p := Placeholder(&session.Snapshot{SessionState: session.StateRunning}); p != "Type to queue a follow-up" {
		t.Errorf("running: %q", p)
	}
	if p := Placeholder(&session.Snapshot{SessionState: session.StateWaiting, PendingApprovals: []session.Approval{{ID: "a"}}}); p != "Answer above, or type to queue" {
		t.Errorf("pending: %q", p)
	}
	if w, ok := Slash("/co", 3); !ok || w != "/co" {
		t.Errorf("slash %q %v", w, ok)
	}
	if _, ok := Slash("/compact now", 9); ok {
		t.Error("slash past the first word")
	}
	if _, ok := Slash("hi /x", 5); ok {
		t.Error("slash not at the start")
	}
}

// Fit leaves an accepted picture as it is and re-encodes one the daemon
// would refuse as JPEG.
func TestFit(t *testing.T) {
	p := pngOf(4, 2)
	if b, mime, err := Fit(p); err != nil || mime != "image/png" || !bytes.Equal(b, p) {
		t.Fatalf("png: %s %v", mime, err)
	}
	var gif bytes.Buffer
	gif.WriteString("GIF89a")
	if _, _, err := Fit(gif.Bytes()); err == nil {
		t.Fatal("a broken GIF fit")
	}
	big := image.NewNRGBA(image.Rect(0, 0, 3000, 1000))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range big.Pix {
		big.Pix[i] = uint8(rng.Uint32()) // noise, so the PNG is big
	}
	var b bytes.Buffer
	png.Encode(&b, big)
	if b.Len() <= maxSend {
		t.Fatalf("the noise PNG is only %d bytes", b.Len())
	}
	out, mime, err := Fit(b.Bytes())
	if err != nil || mime != "image/jpeg" || http.DetectContentType(out) != "image/jpeg" || len(out) > maxSend {
		t.Fatalf("big png: %s %d %v", mime, len(out), err)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(out))
	if cfg.Width != fitSide || cfg.Height != fitSide/3 {
		t.Fatalf("fit to %dx%d", cfg.Width, cfg.Height)
	}
	if th, err := Thumb(p, 96, 128); err != nil || th.Bounds().Dx() != 96 || th.Bounds().Dy() != 48 {
		t.Fatalf("thumb %v %v", th.Bounds(), err)
	}
}

// harness draws a composer through a router, so keys and pastes reach it.
type harness struct {
	t    *testing.T
	c    Composer
	r    input.Router
	ops  op.Ops
	snap session.Snapshot
	clip []byte        // the clipboard's image
	cerr error         // what reading it fails with
	gate chan struct{} // when set, a clipboard read waits for it
	now  time.Time
	key  func(string) bool // the transcript's, nil when it takes no keys
	// answer is the transcript's typed answer to its open question, nil
	// when it takes none.
	answer func(string) bool
	old    bool // the daemon predates ChatSend How "now"
	// paths are the chat's files for @ mentions; a query matches those
	// that contain it. branch and copied are the branch chip's.
	paths  []string
	branch string
	copied []string
	// separate is a daemon that joins the queue (proto.SmartQueueLevel).
	separate bool
	// files is a daemon that saves pasted text to files
	// (proto.PastedFileLevel); reveal is the transcript's Reveal.
	files  bool
	reveal func(Source) bool
}

func newHarness(t *testing.T) *harness {
	slashUsed = map[string]int{} // each test starts with nothing sent
	h := &harness{t: t, snap: session.Snapshot{SessionID: "c1", SessionState: session.StateReady}, now: time.Now()}
	h.frame()
	h.frame()
	if !h.r.Source().Focused(&h.c.ed) {
		t.Fatal("the composer did not take focus")
	}
	return h
}

func (h *harness) env() Env {
	return Env{Chat: "c1", Snap: &h.snap, Width: 960, Focus: true, Images: true, Key: h.key, Answer: h.answer, Now: !h.old, Queue: !h.old, Separate: h.separate, Commands: !h.old,
		Branch: h.branch, Copy: func(s string) { h.copied = append(h.copied, s) }, TextFiles: h.files, Reveal: h.reveal,
		Mentions: func(q string) []string {
			var out []string
			for _, p := range h.paths {
				if strings.Contains(p, q) {
					out = append(out, p)
				}
			}
			return out
		},
		ReadImage: func(context.Context) ([]byte, error) {
			if h.gate != nil {
				<-h.gate
			}
			return h.clip, h.cerr
		}}
}

func (h *harness) frame() []any {
	h.ops.Reset()
	h.now = h.now.Add(10 * time.Millisecond)
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1000, 800)), Now: h.now}
	gtx.Constraints.Min.Y = 0
	h.c.Layout(gtx, theme.Dark(), h.env())
	h.r.Frame(&h.ops)
	return h.c.Messages()
}

func (h *harness) press(k key.Name, mods key.Modifiers) []any {
	h.r.Queue(key.Event{Name: k, Modifiers: mods, State: key.Press})
	return h.frame()
}

func (h *harness) typ(s string) {
	h.c.ed.Insert(s) // as typing does at the caret
	h.frame()
}

// paste is Ctrl+V with text on the clipboard, "" for none.
func (h *harness) paste(text string) {
	h.press("V", key.ModShortcut)
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(text)) }})
	h.frame()
}

// waitImage draws frames until the clipboard's image is in.
func (h *harness) waitImage() {
	for range 200 {
		if h.c.reading == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
		h.frame()
	}
	h.t.Fatal("the image never came")
}

// Enter sends and Shift+Enter adds a line; a pasted image goes in as a
// token and a chip and rides along; while a turn runs Enter queues,
// Escape stops and Shift+Tab cycles the mode.
func TestComposerKeys(t *testing.T) {
	h := newHarness(t)
	if m := h.press(key.NameReturn, 0); len(m) != 0 {
		t.Fatalf("an empty box sent %v", m)
	}
	h.typ("one")
	if m := h.press(key.NameReturn, key.ModShift); len(m) != 0 || h.c.Text() != "one\n" {
		t.Fatalf("Shift+Enter: sent %v, text %q", m, h.c.Text())
	}
	h.typ("two ")
	h.clip = pngOf(8, 6)
	h.paste("")
	h.waitImage()
	if h.c.Text() != "one\ntwo [Image #1] " || len(h.c.Images()) != 1 {
		t.Fatalf("after the image paste: %q, %d images", h.c.Text(), len(h.c.Images()))
	}
	h.paste("three")
	m := h.press(key.NameReturn, 0)
	if len(m) != 1 {
		t.Fatalf("Enter sent %v", m)
	}
	s, ok := m[0].(proto.ChatSend)
	if !ok || s.Chat != "c1" || s.Text != "one\ntwo [Image #1] three" || s.How != "" || len(s.Attachments) != 1 || s.Attachments[0].MimeType != "image/png" {
		t.Fatalf("send %+v", m[0])
	}
	if h.c.Text() != "" || len(h.c.Images()) != 0 {
		t.Fatalf("the box kept %q", h.c.Text())
	}

	// Another request's refusal shows its note and leaves the box alone.
	h.c.Result(Result{Req: proto.ChatSet{Chat: "c1", Mode: ModePlan}, Message: "unsupported"}, h.now)
	if h.c.Text() != "" || h.c.note != "unsupported" {
		t.Fatalf("a refused ChatSet filled the box: %q", h.c.Text())
	}
	// A refused send comes back into the empty box.
	h.c.Result(Result{Req: s, Message: "busy"}, h.now)
	if h.c.Text() != "one\ntwo [Image #1] three" || len(h.c.Images()) != 1 || h.c.note != "busy" {
		t.Fatalf("the refused send is not back: %q", h.c.Text())
	}
	// Backspace after the token takes it whole and leaves the image on
	// the shelf, as T3 Code's chips do; the thumbnail's x then takes it.
	h.c.SetText("look [Image #1]")
	h.frame()
	h.press(key.NameDeleteBackward, 0)
	if h.c.Text() != "look " || len(h.c.Images()) != 1 {
		t.Fatalf("Backspace left %q and %d images", h.c.Text(), len(h.c.Images()))
	}
	h.c.RemoveImage(1)
	if len(h.c.Images()) != 0 {
		t.Fatal("RemoveImage left the image")
	}
	// The chip's x takes its token.
	h.paste("")
	h.waitImage()
	h.c.RemoveImage(2)
	if h.c.Text() != "look " || len(h.c.Images()) != 0 {
		t.Fatalf("removing the chip left %q", h.c.Text())
	}

	// The token goes in where Ctrl+V was pressed, even when typing goes
	// on while the image is read; Enter waits for the image.
	h.c.SetText("a ")
	h.gate = make(chan struct{})
	h.paste("")
	h.typ("b")
	if m := h.press(key.NameReturn, 0); len(m) != 0 || h.c.note == "" {
		t.Fatalf("Enter with an image on its way sent %v", m)
	}
	close(h.gate) // a closed gate never blocks; setting it to nil would race the reader
	h.waitImage()
	if h.c.Text() != "a [Image #3] b" || len(h.c.Images()) != 1 || h.c.Images()[0].Data == nil {
		t.Fatalf("after a slow read: %q", h.c.Text())
	}
	h.c.SetText("")

	h.snap.SessionState = session.StateRunning
	h.typ("later")
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).How != "queue" {
		t.Fatalf("Enter while running sent %+v", m)
	}
	if m := h.press(key.NameEscape, 0); len(m) != 1 || m[0] != (proto.ChatInterrupt{Chat: "c1"}) {
		t.Fatalf("Escape sent %+v", m)
	}
	var modes []string
	for range 3 {
		for _, m := range h.press(key.NameTab, key.ModShift) {
			modes = append(modes, m.(proto.ChatSet).Mode)
		}
	}
	if !slices.Equal(modes, []string{ModeAccept, ModePlan, ModeDefault}) {
		t.Fatalf("Shift+Tab sent %v", modes)
	}
	h.snap.SessionState = session.StateReady
	if m := h.press(key.NameEscape, 0); len(m) != 0 {
		t.Fatalf("Escape while idle sent %v", m)
	}
}

// The main button: the arrow when idle (dim with nothing typed), Stop
// while the chat works with an empty box, and Queue once there is a
// draft, a pending request counting as work.
func TestButtonOf(t *testing.T) {
	idle := &session.Snapshot{SessionState: session.StateReady}
	running := &session.Snapshot{SessionState: session.StateRunning}
	pending := &session.Snapshot{SessionState: session.StateReady, PendingApprovals: []session.Approval{{ID: "a"}}}
	for _, c := range []struct {
		name  string
		s     *session.Snapshot
		draft bool
		want  Button
	}{
		{"idle, empty", idle, false, ButtonSendOff},
		{"idle, text", idle, true, ButtonSend},
		{"running, empty", running, false, ButtonStop},
		{"running, text", running, true, ButtonQueue},
		{"request pending, text", pending, true, ButtonQueue},
	} {
		if got := ButtonOf(c.s, c.draft); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// Queued messages: Edit takes one out of the queue and back into the
// box after the draft, its images renumbered; the x only takes it out;
// either hides the row before the chat drops it.
func TestQueuedRows(t *testing.T) {
	h := newHarness(t)
	img := base64.StdEncoding.EncodeToString(pngOf(4, 4))
	h.snap.SessionState = session.StateRunning
	h.snap.QueuedInputs = []session.QueuedInput{
		{ID: "q1", Text: "first [Image #2] and [Image #1]", Attachments: []session.Attachment{
			{ID: "image-1", Kind: "image", MimeType: "image/png", Data: img},
			{ID: "image-2", Kind: "image", MimeType: "image/png", Data: img},
		}},
		{ID: "q2", Text: "second"},
	}
	h.c.SetText("draft [Image #1]")
	h.c.draft.Add(Image{Data: []byte{1}, Mime: "image/png"})
	h.frame()
	h.c.click("qedit:q1").Click()
	m := h.frame()
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "remove", ID: "q1"}) {
		t.Fatalf("Edit sent %+v", m)
	}
	if want := "draft [Image #1]\nfirst [Image #3] and [Image #2]"; h.c.Text() != want {
		t.Fatalf("pulled back %q, want %q", h.c.Text(), want)
	}
	if got := numbers(h.c.Images()); !slices.Equal(got, []int{1, 3, 2}) {
		t.Fatalf("images %v", got)
	}
	h.c.click("qrm:q2").Click()
	if m := h.frame(); len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "remove", ID: "q2"}) {
		t.Fatalf("remove sent %+v", m)
	}
	if !h.c.q.gone["q1"] || !h.c.q.gone["q2"] {
		t.Fatal("removed rows still show")
	}
	// A refused removal brings its row back.
	h.c.Result(Result{Req: proto.ChatQueue{Chat: "c1", Action: "remove", ID: "q2"}, Message: "no queued message q2"}, h.now)
	if !h.c.q.gone["q1"] || h.c.q.gone["q2"] {
		t.Fatalf("after a refused removal: %v", h.c.q.gone)
	}
	h.snap.QueuedInputs = nil
	h.frame()
	if len(h.c.q.gone) != 0 {
		t.Fatal("rows the chat dropped are still remembered")
	}
	// Enter with a draft while running queues and never interrupts.
	m = h.press(key.NameReturn, 0)
	if len(m) != 1 || m[0].(proto.ChatSend).How != "queue" {
		t.Fatalf("Enter sent %+v", m)
	}
	// Once the send lands, a later refusal does not bring it back.
	h.c.Result(Result{Req: m[0]}, h.now)
	h.c.Result(Result{Req: proto.ChatSend{Chat: "c1"}, Message: "busy"}, h.now)
	if h.c.Text() != "" {
		t.Fatalf("a landed send came back: %q", h.c.Text())
	}
}

// TestImageReadFails: a paste whose image can't be read takes its token
// out and keeps saying why, past the frame that sees the token go.
func TestImageReadFails(t *testing.T) {
	h := newHarness(t)
	h.typ("look ")
	h.cerr = errors.New("pasting an image needs xclip; install it")
	h.paste("")
	h.waitImage()
	h.frame()
	h.frame()
	if h.c.Text() != "look " || len(h.c.Images()) != 0 {
		t.Fatalf("text %q, %d images", h.c.Text(), len(h.c.Images()))
	}
	if !strings.Contains(h.c.note, "needs xclip") {
		t.Fatalf("note %q, want why the image did not attach", h.c.note)
	}
	h.typ("x")
	if h.c.note != "" {
		t.Fatalf("typing kept the note %q", h.c.note)
	}
}

// TestNoImages: a daemon too old for attachments gets a note, not a token.
func TestNoImages(t *testing.T) {
	var c Composer
	c.Reading(Env{}, func(context.Context) ([]byte, error) { return pngOf(4, 4), nil })
	if c.Text() != "" || len(c.draft.Images) != 0 || c.note == "" {
		t.Fatalf("text %q, images %d, note %q", c.Text(), len(c.draft.Images), c.note)
	}
}

// TestTranscriptKeys: a digit typed into the empty box, or Enter on it,
// goes to the transcript; one it does not take stays, and a digit after
// text is only text.
func TestTranscriptKeys(t *testing.T) {
	h := newHarness(t)
	var got []string
	h.key = func(name string) bool { got = append(got, name); return name != "5" }
	h.r.Queue(key.EditEvent{Text: "1"})
	h.frame()
	if h.c.Text() != "" {
		t.Fatalf("the box kept %q", h.c.Text())
	}
	h.press(key.NameReturn, 0)
	h.r.Queue(key.EditEvent{Text: "5"})
	h.frame()
	if h.c.Text() != "5" {
		t.Fatalf("an untaken digit left %q", h.c.Text())
	}
	h.r.Queue(key.EditEvent{Range: key.Range{Start: 1, End: 1}, Text: "2"})
	h.frame()
	if want := []string{"1", string(key.NameReturn), "5"}; !slices.Equal(got, want) || h.c.Text() != "52" {
		t.Fatalf("keys %v, text %q", got, h.c.Text())
	}
}

// TestEffortChip: the effort chip reads "Effort" while unset, the chat's
// effort once set, and a pick at once; a pick sends ChatSet, and the
// model popover lists no efforts.
func TestEffortChip(t *testing.T) {
	for e, want := range map[string]string{"": "Effort", "default": "Effort", "high": "High", "xhigh": "Extra high", "max": "Max"} {
		if got := EffortName(e); got != want {
			t.Errorf("EffortName(%q) = %q, want %q", e, got, want)
		}
	}
	h := newHarness(t)
	h.snap.Provider = model.ProviderClaude
	h.frame()
	h.c.modelBtn.Click()
	h.frame()
	if !h.c.menu || h.c.which != &h.c.modelBtn {
		t.Fatal("the model chip opened no model popover")
	}
	for k := range h.c.items {
		if strings.HasPrefix(k, "effort:") {
			t.Fatalf("the model popover lists %s", k)
		}
	}
	h.c.effortBtn.Click()
	h.frame()
	if !h.c.menu || h.c.which != &h.c.effortBtn {
		t.Fatal("the effort chip did not switch to its popover")
	}
	h.frame()
	h.c.click("effort:max").Click()
	m := h.frame()
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatSet{Chat: "c1", Effort: "max"}) {
		t.Fatalf("an effort pick sent %+v", m)
	}
	if h.c.menu {
		t.Fatal("the popover stayed open after a pick")
	}
	if got := EffortName(h.c.effort.show(deref(h.snap.Effort), h.now)); got != "Max" {
		t.Fatalf("held label %q, want Max", got)
	}
	h.snap.Effort = ptr("high")
	if got := EffortName(h.c.effort.show(deref(h.snap.Effort), h.now)); got != "High" {
		t.Fatalf("label after the chat reports %q, want High", got)
	}
}

func ptr[T any](v T) *T { return &v }

// TestBypassMode: bypass reads red with a shield and its warning, and
// Shift+Tab reaches it only in a chat that has run in it.
func TestBypassMode(t *testing.T) {
	th := theme.Dark()
	if ModeName(ModeBypass) != "Bypass permissions" {
		t.Errorf("label %q", ModeName(ModeBypass))
	}
	col, icon, tip := modeLook(th, ModeBypass)
	if col != th.Readable(th.Red, th.Surface) || icon != "shield" || !strings.HasPrefix(tip, "Bypass permissions: ") {
		t.Errorf("bypass looks %v %q %q", col, icon, tip)
	}
	if col, icon, _ := modeLook(th, ModeDefault); col == th.Readable(th.Red, th.Surface) || icon != "circle" {
		t.Errorf("default looks %v %q", col, icon)
	}
	cycle := func(h *harness, n int) []string {
		var got []string
		for range n {
			m := h.press(key.NameTab, key.ModShift)
			got = append(got, m[0].(proto.ChatSet).Mode)
		}
		return got
	}
	h := newHarness(t)
	if got := cycle(h, 4); !slices.Equal(got, []string{ModeAccept, ModePlan, ModeDefault, ModeAccept}) {
		t.Fatalf("a chat never in bypass cycles %v", got)
	}
	h = newHarness(t)
	h.snap.PermissionMode = ModeBypass
	h.frame()
	if got := cycle(h, 5); !slices.Equal(got, []string{ModeDefault, ModeAccept, ModePlan, ModeBypass, ModeDefault}) {
		t.Fatalf("a chat started in bypass cycles %v", got)
	}
}

// TestTranscriptKeysOneFrame: a digit and Enter that reach the empty box
// in one frame, as when the window was idle, go to the transcript in the
// order they were pressed.
func TestTranscriptKeysOneFrame(t *testing.T) {
	h := newHarness(t)
	var got []string
	h.key = func(name string) bool { got = append(got, name); return true }
	h.r.Queue(key.EditEvent{Text: "3"}, key.Event{Name: key.NameReturn, State: key.Press})
	h.frame()
	if want := []string{"3", string(key.NameReturn)}; !slices.Equal(got, want) || h.c.Text() != "" {
		t.Fatalf("keys %v, text %q: want 3 then Enter", got, h.c.Text())
	}
}

// TestAgyChoices: an Antigravity chat's chips offer agy's models, efforts
// and modes, in the names its TUI and ACP options take.
func TestAgyChoices(t *testing.T) {
	agy := model.ProviderAntigravity
	if m := ModelsOf(agy); !slices.Contains(m, "gemini-4-neon") || slices.Contains(m, "opus") {
		t.Errorf("models %v", m)
	}
	if e := EffortsOf(agy); !slices.Equal(e, []string{"low", "medium", "high", "xhigh"}) || EffortsOf(model.ProviderCodex) != nil {
		t.Errorf("efforts %v", e)
	}
	if got := NextModeOf(agy, "plan", false); got != ModeDefault {
		t.Errorf("after plan: %q", got)
	}
	if got := NextModeOf(agy, "accept-edits", true); got != "auto" {
		t.Errorf("after accept-edits: %q", got)
	}
	for id, want := range map[string]string{"gemini-4-neon": "Gemini 4 Neon", "gemini-3.8-flash": "Gemini 3.8 Flash", "claude-opus-5-5": "Opus 5.5"} {
		if got := ModelName(id); got != want {
			t.Errorf("ModelName(%q) = %q, want %q", id, got, want)
		}
	}
	if ModeName("accept-edits") != "Accept edits" {
		t.Error("accept-edits' label")
	}
}

// TestModeUnknown: an agy pane chat shows no mode chip until it has read
// agy's footer; other chats always show theirs.
func TestModeUnknown(t *testing.T) {
	for _, c := range []struct {
		s    session.Snapshot
		want bool
	}{
		{session.Snapshot{PaneID: "p", Provider: model.ProviderAntigravity}, true},
		{session.Snapshot{PaneID: "p", Provider: model.ProviderAntigravity, PermissionMode: "auto"}, false},
		{session.Snapshot{Provider: model.ProviderAntigravity}, false},
		{session.Snapshot{PaneID: "p", Provider: model.ProviderClaude}, false},
	} {
		if got := modeUnknown(&c.s); got != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
	}
}

// TestComposerAnswers: a draft typed while a question waits answers it on
// Enter. A draft written before the question came is queued as the
// message it was meant to be, never eaten as an answer, until an edit
// after the card came; so are a slash command, an image, Ctrl+Enter, and
// any draft while an approval waits as well.
func TestComposerAnswers(t *testing.T) {
	h := newHarness(t)
	var got []string
	h.answer = func(s string) bool { got = append(got, s); return true }
	h.key = func(string) bool { return false }
	ask := func(id string) {
		h.snap.SessionState = session.StateWaiting
		h.snap.PendingUserInputs = []session.InputRequest{{ID: id, Questions: []session.Question{{ID: "q", Options: []session.PromptOption{{Label: "Yes"}}}}}}
		h.frame()
	}
	queued := func(text string, m []any) {
		t.Helper()
		if len(m) != 1 || m[0].(proto.ChatSend).Text != text || m[0].(proto.ChatSend).How == "" {
			t.Fatalf("want %q queued, got %v (answers %q)", text, m, got)
		}
	}

	h.typ("follow up on the tests")
	ask("i1")
	if Placeholder(&h.snap) != "Pick an answer above, or type your own" || h.c.Text() != "follow up on the tests" {
		t.Fatalf("placeholder %q, box %q", Placeholder(&h.snap), h.c.Text())
	}
	queued("follow up on the tests", h.press(key.NameReturn, 0))

	h.typ("Medium")
	if m := h.press(key.NameReturn, 0); len(m) != 0 || !slices.Equal(got, []string{"Medium"}) || h.c.Text() != "" {
		t.Fatalf("typed answer: sent %v, answers %q, box %q", m, got, h.c.Text())
	}

	ask("i2") // a second card while the first one's draft is gone
	h.typ("/usr/local/bin")
	h.press(key.NameReturn, 0)
	h.typ("/compact")
	queued("/compact", h.press(key.NameReturn, 0))
	h.typ("steer it")
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 1 || m[0].(proto.ChatSend).How != "now" {
		t.Fatalf("Ctrl+Enter: %v", m)
	}
	h.snap.PendingApprovals = []session.Approval{{ID: "a1", RequestType: session.RequestCommand}}
	h.typ("both wait")
	queued("both wait", h.press(key.NameReturn, 0))
	h.snap.PendingApprovals = nil
	h.typ("look ")
	h.clip = pngOf(4, 4)
	h.paste("")
	h.waitImage()
	if m := h.press(key.NameReturn, 0); len(m) != 1 || len(m[0].(proto.ChatSend).Attachments) != 1 {
		t.Fatalf("a draft with an image: %v", m)
	}
	if want := []string{"Medium", "/usr/local/bin"}; !slices.Equal(got, want) {
		t.Fatalf("answers %q, want %q", got, want)
	}

	// An earlier card's draft does not answer a newer card.
	h.typ("for the old one")
	ask("i3")
	queued("for the old one", h.press(key.NameReturn, 0))
	// A question that refuses the text (already sent) leaves a queued send.
	h.answer = func(string) bool { return false }
	h.typ("late")
	queued("late", h.press(key.NameReturn, 0))

	// A daemon from before ChatSend How "now" and the queue: ChatAnswer is
	// older than both, so the typed answer still goes as one.
	got, h.old = nil, true
	h.answer = func(s string) bool { got = append(got, s); return true }
	ask("i4")
	h.typ("Old daemon")
	if m := h.press(key.NameReturn, 0); len(m) != 0 || !slices.Equal(got, []string{"Old daemon"}) {
		t.Fatalf("older daemon: sent %v, answers %q", m, got)
	}
}
