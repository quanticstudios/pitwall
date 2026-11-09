package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/drawbench"
)

func TestDrawBenchWindow(t *testing.T) {
	t.Setenv("PITWALL_DRAW_BENCH", "")
	if b, err := newDrawBench(); b != nil || err != nil {
		t.Fatal(b, err)
	}
	path := filepath.Join(t.TempDir(), "frames.json")
	t.Setenv("PITWALL_DRAW_BENCH", path)
	t.Setenv("PITWALL_DRAW_BENCH_DURATION", "1s")
	b, err := newDrawBench()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i, elapsed := range []time.Duration{time.Millisecond, 3 * time.Millisecond, 50 * time.Millisecond} {
		if err := b.record(start.Add(time.Duration(i)*500*time.Millisecond), elapsed, 1.5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".ready"); err != nil {
		t.Fatal(err)
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got drawbench.Frames
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Drawn != 2 || got.P50MS != 1 || got.P95MS != 3 || got.Scale != 1.5 {
		t.Fatal(got)
	}
	t.Setenv("PITWALL_DRAW_BENCH_DURATION", "0s")
	if _, err := newDrawBench(); err == nil {
		t.Fatal("accepted zero duration")
	}
}
