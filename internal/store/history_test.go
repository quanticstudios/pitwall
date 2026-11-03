package store

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestHistoryInStateAndSnapshots(t *testing.T) {
	text := "\x1b[31mred\x1b[0m\r\nlast\r\n"
	h, err := PackHistory(80, text, time.Date(2026, 11, 3, 14, 32, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	s := model.State{Workspaces: []model.Workspace{{ID: "w"}}, Panes: []model.Pane{{ID: "p", History: h}}}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	check := func(path string) {
		t.Helper()
		got, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		text2, err := UnpackHistory(got.Panes[0].History)
		if err != nil || text2 != text || got.Panes[0].History.Cols != 80 || !got.Panes[0].History.SavedAt.Equal(h.SavedAt) {
			t.Fatalf("round trip %q, %v", text2, err)
		}
	}
	check(path)
	names := ListSnapshots(path)
	if len(names) != 1 {
		t.Fatalf("snapshots %v", names)
	}
	check(filepath.Join(SnapshotDir(path), names[0]))
	// The same format with no optional history remains readable.
	s.Panes[0].History = nil
	data, _ := json.Marshal(snapshot{formatVersion, &s})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := Load(path)
	if err != nil || old.Panes[0].History != nil {
		t.Fatalf("old state: %v", err)
	}
}

func TestHistoryRejectsDamageAndSize(t *testing.T) {
	h, err := PackHistory(80, "text\r\n", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte("bad"), h.Data[:len(h.Data)-4]} {
		bad := *h
		bad.Data = data
		if _, err := UnpackHistory(&bad); err == nil {
			t.Fatal("accepted damaged history")
		}
	}
	bad := *h
	bad.Cols = 1001
	if _, err := UnpackHistory(&bad); err == nil {
		t.Fatal("accepted huge width")
	}
	if _, err := PackHistory(80, strings.Repeat("x", HistoryBytes+1), time.Now()); err == nil {
		t.Fatal("accepted huge text")
	}
	var bomb bytes.Buffer
	w := gzip.NewWriter(&bomb)
	w.Write([]byte(strings.Repeat("x", HistoryBytes+1)))
	w.Close()
	bad = *h
	bad.Data = bomb.Bytes()
	if _, err := UnpackHistory(&bad); err == nil {
		t.Fatal("accepted oversized decompression")
	}
}

func BenchmarkSaveHistory(b *testing.B) {
	var text strings.Builder
	for n := range 2000 {
		fmt.Fprintf(&text, "\x1b[32m%04d\x1b[0m compiled internal/example in %d ms: %s\r\n", n, n%500, strings.Repeat("x", 55))
	}
	path := filepath.Join(b.TempDir(), "state.json")
	s := model.State{Workspaces: []model.Workspace{{ID: "w"}}, Panes: []model.Pane{{ID: "p"}}}
	for _, history := range []bool{false, true} {
		b.Run(fmt.Sprint(history), func(b *testing.B) {
			for b.Loop() {
				if history {
					var err error
					s.Panes[0].History, err = PackHistory(120, text.String(), time.Unix(1, 0))
					if err != nil {
						b.Fatal(err)
					}
				}
				if err := Save(path, s); err != nil {
					b.Fatal(err)
				}
			}
			fi, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(fi.Size()), "state-bytes")
		})
	}
}

func TestHistoryAllowsOnlyTextAndSGR(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"界é\r\n\x1b[38;2;1;2;3mred\x1b[0m", true},
		{"\x1b]52;c;c2VjcmV0\a", false}, {"\x1b[6n", false}, {"\x1b[2J", false},
		{"\a", false}, {"\xc2\x9b31m", false}, {"\xff", false},
	} {
		h, err := PackHistory(80, tc.text, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, err = UnpackHistory(h)
		if (err == nil) != tc.want {
			t.Errorf("%q: error %v", tc.text, err)
		}
	}
}
