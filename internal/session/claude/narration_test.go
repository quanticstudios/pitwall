package claude

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// pb is a protobuf length-delimited field n holding b.
func pb(n uint64, b []byte) []byte {
	out := binary.AppendUvarint(nil, n<<3|2)
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

// signature is a thinking block's signature whose kind is kind, wrapped
// in other fields as the server's are.
func signature(kind string) string {
	inner := append(binary.AppendUvarint(nil, 3<<3), 7) // a varint field first
	inner = append(inner, pb(8, []byte(kind))...)
	msg := append(pb(1, []byte("x")), pb(2, append(pb(1, inner), pb(4, []byte("y"))...))...)
	return base64.StdEncoding.EncodeToString(msg)
}

// TestNarration reads a signature's block kind as Claude Code does.
func TestNarration(t *testing.T) {
	for sig, want := range map[string]bool{
		signature("narration"): true,
		signature("thinking"):  false,
		"":                     false,
		"not base64!":          false,
		base64.StdEncoding.EncodeToString([]byte{0x12, 0x40, 1}): false, // a length past the end
	} {
		if narration(sig) != want {
			t.Errorf("narration(%q) = %v, want %v", sig, !want, want)
		}
	}
}

// TestSummaryBlock: a thinking block the server marked as narration is a
// summary item, whole in the transcript and after streaming in
// stream-json, where its deltas show nothing; plain thinking stays thinking.
func TestSummaryBlock(t *testing.T) {
	line := func(sig, text string) string {
		return `{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T17:23:00Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"` + text + `","signature":"` + sig + `"}]}}`
	}
	tr := NewTranscript()
	snap := apply(t, base(), tr.Line([]byte(`{"type":"user","uuid":"p1","timestamp":"2026-11-20T17:22:59Z","origin":{"kind":"human"},"message":{"role":"user","content":"go"}}`)))
	snap = apply(t, snap, tr.Line([]byte(line(signature("narration"), "Checked the docs; the fix is in main."))))
	snap = apply(t, snap, tr.Line([]byte(strings.Replace(line(signature("thinking"), "Hmm."), `"m1"`, `"m2"`, 1))))
	var got []string
	for _, it := range snap.Content {
		if it.Kind == session.KindReasoningText {
			got = append(got, it.Phase+":"+it.Text)
		}
	}
	if strings.Join(got, "|") != "summary:Checked the docs; the fix is in main.|:Hmm." {
		t.Fatalf("reasoning items %q", got)
	}

	s := newStream("")
	snap = base()
	for _, l := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}},"api_message_id":"m1"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Ran the tests."}},"api_message_id":"m1"}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"Ran the tests.","signature":"` + signature("narration") + `"}]}}`,
	} {
		snap = apply(t, snap, s.line([]byte(l)))
		// Claude Code streams a summary's text as thinking deltas before
		// the line that says it is a summary: the row shows none of it.
		if len(snap.Content) == 1 && snap.Content[0].Phase == "" && snap.Content[0].Text != "" {
			t.Fatalf("summary text %q streamed into a thinking row", snap.Content[0].Text)
		}
	}
	if len(snap.Content) != 1 || snap.Content[0].Phase != session.PhaseSummary || snap.Content[0].Text != "Ran the tests." {
		t.Fatalf("streamed summary %+v", snap.Content)
	}
}

// TestNarrationPaneLine: a narration line as Claude Code 2.1.329 wrote it
// to a TUI pane's transcript, its real signature kept and its text
// shortened, is a summary item, not thinking.
func TestNarrationPaneLine(t *testing.T) {
	line, err := os.ReadFile("testdata/narration-pane.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	snap := apply(t, base(), tr.Line([]byte(`{"type":"user","uuid":"p1","timestamp":"2026-11-20T21:30:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"go"}}`)))
	snap = apply(t, snap, tr.Line(bytes.TrimSpace(line)))
	var got []string
	for _, it := range snap.Content {
		if it.Kind == session.KindReasoningText {
			got = append(got, it.Phase+":"+it.Text)
		}
	}
	if want := "summary:Checking whether stored snapshots keep the stale classification."; len(got) != 1 || got[0] != want {
		t.Fatalf("reasoning items %q, want %q", got, want)
	}
}
