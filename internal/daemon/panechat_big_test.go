package daemon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// TestPaneChatBigTranscript measures the pane chat of the transcript at
// $PITWALL_BIG_TRANSCRIPT: its first read, each page back to the file's
// start and a cut item fetched whole, by their time, their frames and the
// process's peak memory. It skips without one.
func TestPaneChatBigTranscript(t *testing.T) {
	path := os.Getenv("PITWALL_BIG_TRANSCRIPT")
	if path == "" {
		t.Skip("set PITWALL_BIG_TRANSCRIPT to a Claude Code transcript")
	}
	d, _, _ := remoteDaemon(t)
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()

	a, b := net.Pipe()
	sizes := make(chan int, 64)
	go func() {
		var h [4]byte
		for {
			if _, err := io.ReadFull(b, h[:]); err != nil {
				close(sizes)
				return
			}
			n := binary.BigEndian.Uint32(h[:])
			io.CopyN(io.Discard, b, int64(n))
			sizes <- int(n)
		}
	}()
	conn := proto.NewConn(a)
	send := func(r any) int {
		if err := conn.Send(r); err != nil {
			t.Fatalf("send: %v", err)
		}
		return <-sizes
	}
	start := time.Now()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"})
	took := time.Since(start)
	frame := send(r)
	t.Logf("first read %v, frame %d bytes, peak RSS %s", took.Round(time.Millisecond), frame, peakRSS())
	out, ok := r.(proto.ChatDeltas)
	if !ok {
		t.Fatalf("first read: %+v", r)
	}
	var cut string
	pages, items, biggest, slowest := 0, 0, 0, time.Duration(0)
	for before := out.Older; before > 0; pages++ {
		start := time.Now()
		r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a", Before: before})
		slowest = max(slowest, time.Since(start))
		p, ok := r.(proto.ChatDeltas)
		if !ok || p.Page != before || p.Older >= before {
			t.Fatalf("page before %d: %+v", before, r)
		}
		biggest = max(biggest, send(p))
		var s session.Snapshot
		json.Unmarshal(p.Snapshot, &s)
		items += len(s.Content)
		for _, it := range s.Content {
			if it.Cut && cut == "" {
				cut = it.ID
			}
		}
		before = p.Older
	}
	t.Logf("%d pages, %d items, slowest %v, largest frame %d bytes, peak RSS %s", pages, items, slowest.Round(time.Millisecond), biggest, peakRSS())
	if cut != "" {
		start := time.Now()
		r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a", Item: cut})
		took := time.Since(start)
		it, ok := r.(proto.ChatDeltas)
		if !ok || it.Item == nil {
			t.Fatalf("item %s: %+v", cut, r)
		}
		t.Logf("item %s whole: %v, frame %d bytes", cut, took.Round(time.Millisecond), send(it))
	}
}

// peakRSS is the process's VmHWM line from /proc/self/status.
func peakRSS() string {
	b, _ := os.ReadFile("/proc/self/status")
	for l := range strings.Lines(string(b)) {
		if strings.HasPrefix(l, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "unknown"
}
