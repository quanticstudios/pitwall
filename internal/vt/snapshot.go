package vt

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
)

// SnapshotVersion changes whenever the handoff state or backend encoding is
// incompatible. A receiver must refuse an unknown version and restart instead.
const SnapshotVersion uint32 = 1

const maxSnapshotBytes = 64 << 20

// ErrSnapshotVersion tells the handoff caller to fall back to restarting.
var ErrSnapshotVersion = errors.New("unsupported terminal snapshot version")

type savedRun struct {
	Col    uint32
	Attrs  Attr
	FG, BG Color
	Link   string
}

type savedLine struct {
	Text  string
	Cells []uint16
	Runs  []savedRun
	Flags xvt.LineFlags
}

type savedMode struct {
	Number    int
	ANSI, Set bool
}

type savedState struct {
	Backend       []byte
	History       []savedLine
	HistoryMax    int
	Pushed        uint64
	Title, Cwd    string
	Hidden        bool
	Shape         CursorShape
	Modes         []savedMode
	Kitty         [2][]uint8
	OSCState      int
	OSCBuffer     []byte
	Term          byte
	Last          *Grid
	SyncRemaining time.Duration
}

// Save serializes the emulator for a daemon handoff without changing Emulator's
// display Snapshot contract. Stop the pane's PTY reader before calling Save,
// then resume it on the restored emulator. Pending escape/UTF-8 bytes are kept.
// Other Emulator implementations may opt in through encoding.BinaryMarshaler.
func Save(e Emulator) ([]byte, error) {
	if m, ok := e.(encoding.BinaryMarshaler); ok {
		return m.MarshalBinary()
	}
	return nil, errors.New("terminal emulator does not support handoff snapshots")
}

func (t *emulator) MarshalBinary() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	backend, err := t.e.MarshalBinary()
	if err != nil {
		return nil, err
	}
	st := t.st
	s := savedState{Backend: backend, HistoryMax: st.hist.max, Pushed: st.hist.pushed,
		Title: st.title, Cwd: st.cwd, Hidden: st.hidden, Shape: st.shape, Kitty: st.kitty,
		OSCState: t.osc.state, OSCBuffer: t.osc.buf, Term: st.term, Last: st.last}
	if st.syncing {
		s.SyncRemaining = max(0, syncTimeout-time.Since(st.syncStart))
	}
	for m, set := range st.modes {
		_, isANSI := m.(ansi.ANSIMode)
		s.Modes = append(s.Modes, savedMode{m.Mode(), isANSI, set})
	}
	for i := range st.hist.len() {
		l := st.hist.at(i)
		out := savedLine{Text: l.text, Cells: l.cells, Flags: l.flags}
		for _, r := range l.runs {
			out.Runs = append(out.Runs, savedRun{r.col, r.attrs, r.fg, r.bg, r.link})
		}
		s.History = append(s.History, out)
	}
	var b bytes.Buffer
	b.WriteString("PWVT")
	_ = binary.Write(&b, binary.BigEndian, SnapshotVersion)
	if err := gob.NewEncoder(&b).Encode(s); err != nil {
		return nil, fmt.Errorf("encode terminal snapshot: %w", err)
	}
	if b.Len() > maxSnapshotBytes {
		return nil, errors.New("terminal snapshot exceeds handoff size limit")
	}
	return b.Bytes(), nil
}

// Restore builds an independent emulator with a new PTY reply writer. Callers
// must reattach dirty/bell/clipboard/notification callbacks on the result.
// The snapshot is a local daemon protocol, not an untrusted file format.
func Restore(data []byte, reply io.Writer) (Emulator, error) {
	if len(data) < 8 || string(data[:4]) != "PWVT" {
		return nil, errors.New("invalid terminal snapshot header")
	}
	if v := binary.BigEndian.Uint32(data[4:8]); v != SnapshotVersion {
		return nil, fmt.Errorf("%w: %d", ErrSnapshotVersion, v)
	}
	if len(data) > maxSnapshotBytes {
		return nil, errors.New("terminal snapshot exceeds handoff size limit")
	}
	var s savedState
	if err := gob.NewDecoder(bytes.NewReader(data[8:])).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode terminal snapshot: %w", err)
	}
	h := history{max: s.HistoryMax, pushed: s.Pushed}
	if h.max < 0 || len(s.History) > h.limit() || s.Pushed < uint64(len(s.History)) ||
		s.OSCState < 0 || s.OSCState > 3 || len(s.OSCBuffer) > oscMax ||
		s.SyncRemaining < 0 || s.SyncRemaining > syncTimeout {
		return nil, errors.New("invalid terminal snapshot state")
	}
	for _, l := range s.History {
		n := len(l.Text)
		if l.Cells != nil {
			n = 0
			for _, c := range l.Cells {
				n += int(c >> 2)
			}
			if n != len(l.Text) {
				return nil, errors.New("invalid terminal history cell lengths")
			}
			n = len(l.Cells)
		}
		out := line{text: l.Text, cells: l.Cells, flags: l.Flags}
		for i, r := range l.Runs {
			if int(r.Col) >= n || i == 0 && r.Col != 0 || i > 0 && r.Col <= l.Runs[i-1].Col {
				return nil, errors.New("invalid terminal history style run")
			}
			out.runs = append(out.runs, run{r.Col, r.Attrs, r.FG, r.BG, r.Link})
		}
		if n > 0 && len(out.runs) == 0 {
			return nil, errors.New("missing terminal history style run")
		}
		h.lines = append(h.lines, out)
	}
	for _, stack := range s.Kitty {
		if len(stack) > kittyStackMax {
			return nil, errors.New("invalid terminal keyboard stack")
		}
	}
	t := New(1, 1, reply).(*emulator)
	if err := t.e.UnmarshalBinary(s.Backend); err != nil {
		_ = t.Close()
		return nil, err
	}
	st := t.st
	st.hist, st.title, st.cwd = h, s.Title, s.Cwd
	st.hidden, st.shape, st.kitty, st.term, st.last = s.Hidden, s.Shape, s.Kitty, s.Term, s.Last
	st.modes = make(map[ansi.Mode]bool)
	for _, m := range s.Modes {
		var mode ansi.Mode = ansi.DECMode(m.Number)
		if m.ANSI {
			mode = ansi.ANSIMode(m.Number)
		}
		st.modes[mode] = m.Set
	}
	st.syncing = st.modes[modeSync]
	st.syncStart = time.Now().Add(s.SyncRemaining - syncTimeout)
	t.osc = oscFilter{state: s.OSCState, buf: s.OSCBuffer}
	return t, nil
}
