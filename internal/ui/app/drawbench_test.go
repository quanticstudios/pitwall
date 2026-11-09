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
		b.submitted = start.Add(time.Duration(i)*500*time.Millisecond + elapsed)
		if err := b.record(start.Add(time.Duration(i)*500*time.Millisecond), elapsed, time.Second, 1.5); err != nil {
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
	if got.WaitP95MS != 1000 || got.Timing != "layout-submit-v1" {
		t.Fatal(got)
	}
	t.Setenv("PITWALL_DRAW_BENCH_DURATION", "0s")
	if _, err := newDrawBench(); err == nil {
		t.Fatal("accepted zero duration")
	}
}

func TestDrawBenchConsumesSubmission(t *testing.T) {
	b := &frameBench{path: filepath.Join(t.TempDir(), "frames.json"), duration: time.Second}
	start := time.Now()
	if err := b.record(start, time.Millisecond, time.Millisecond, 1); err != nil {
		t.Fatal(err)
	}
	if len(b.samples) != 0 || !b.start.IsZero() {
		t.Fatal("recorded a frame without a submission")
	}
	b.submitted = start.Add(time.Millisecond)
	if err := b.record(start, time.Millisecond, time.Millisecond, 1); err != nil {
		t.Fatal(err)
	}
	if !b.submitted.IsZero() {
		t.Fatal("kept a consumed submission")
	}
	if err := b.record(start.Add(time.Millisecond), time.Millisecond, time.Millisecond, 1); err != nil {
		t.Fatal(err)
	}
	if len(b.samples) != 1 || len(b.waits) != 1 {
		t.Fatal("reused a consumed submission")
	}
}
