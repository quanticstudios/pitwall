package app

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/quanticstudios/pitwall/internal/drawbench"
)

// newDrawBench allocates counters only for an explicitly requested benchmark.
func newDrawBench() (*frameBench, error) {
	path := os.Getenv("PITWALL_DRAW_BENCH")
	if path == "" {
		return nil, nil
	}
	seconds, err := time.ParseDuration(os.Getenv("PITWALL_DRAW_BENCH_DURATION"))
	if err != nil || seconds <= 0 {
		return nil, fmt.Errorf("invalid drawing benchmark duration")
	}
	return &frameBench{path: path, duration: seconds}, nil
}

type frameBench struct {
	path      string
	duration  time.Duration
	start     time.Time
	samples   []time.Duration
	waits     []time.Duration
	submitted time.Time
	scale     float32
}

func (b *frameBench) markSubmitted() { b.submitted = time.Now() }

func (b *frameBench) record(start time.Time, elapsed, wait time.Duration, scale float32) error {
	if b.submitted.IsZero() {
		return nil
	}
	b.submitted = time.Time{}
	if b.start.IsZero() {
		b.start, b.scale = start, scale
		if err := os.WriteFile(b.path+".ready", []byte(start.Format(time.RFC3339Nano)), 0o600); err != nil {
			return err
		}
	}
	if start.Sub(b.start) < b.duration {
		b.samples = append(b.samples, elapsed)
		b.waits = append(b.waits, wait)
	}
	return nil
}

func (b *frameBench) save() error {
	f := drawbench.Summarize(b.samples)
	wait := drawbench.Summarize(b.waits)
	f.Timing = "layout-submit-v1"
	f.WaitP50MS, f.WaitP95MS, f.WaitMaxMS = wait.P50MS, wait.P95MS, wait.MaxMS
	f.Scale = b.scale
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.path, append(data, '\n'), 0o600)
}
