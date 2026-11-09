// Package drawbench holds the window benchmark's result format and counter math.
package drawbench

import (
	"fmt"
	"io"
	"math"
	"slices"
	"time"
)

type Frames struct {
	Timing    string  `json:"timing"`
	Drawn     int     `json:"drawn"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
	MaxMS     float64 `json:"max_ms"`
	Scale     float32 `json:"scale"`
	WaitP50MS float64 `json:"wait_p50_ms"`
	WaitP95MS float64 `json:"wait_p95_ms"`
	WaitMaxMS float64 `json:"wait_max_ms"`
}

type Result struct {
	Workload      string  `json:"workload"`
	Seconds       float64 `json:"seconds"`
	Build         string  `json:"build"`
	VSync         string  `json:"vsync"`
	Frames        Frames  `json:"frames"`
	GUISeconds    float64 `json:"gui_cpu_seconds"`
	DaemonSeconds float64 `json:"daemon_cpu_seconds"`
}

// Summarize uses nearest-rank percentiles. An empty sample has zero timings.
func Summarize(samples []time.Duration) Frames {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	f := Frames{Drawn: len(sorted)}
	if len(sorted) > 0 {
		ms := func(p float64) float64 {
			return float64(sorted[int(math.Ceil(p*float64(len(sorted))))-1]) / float64(time.Millisecond)
		}
		f.P50MS, f.P95MS, f.MaxMS = ms(.5), ms(.95), ms(1)
	}
	return f
}

func Compare(w io.Writer, a, b Result) error {
	if a.Workload != b.Workload || a.Seconds != b.Seconds || a.Frames.Scale != b.Frames.Scale || a.Frames.Timing != b.Frames.Timing || a.VSync != b.VSync {
		return fmt.Errorf("workload, seconds, scale, timing and vsync must match")
	}
	if _, err := fmt.Fprintf(w, "build\t%s\t%s\nmetric\ta\tb\tdelta (b-a)\n", a.Build, b.Build); err != nil {
		return err
	}
	for _, row := range []struct {
		name string
		a, b float64
	}{
		{"frames", float64(a.Frames.Drawn), float64(b.Frames.Drawn)},
		{"p50_ms", a.Frames.P50MS, b.Frames.P50MS},
		{"p95_ms", a.Frames.P95MS, b.Frames.P95MS},
		{"max_ms", a.Frames.MaxMS, b.Frames.MaxMS},
		{"wait_p50_ms", a.Frames.WaitP50MS, b.Frames.WaitP50MS},
		{"wait_p95_ms", a.Frames.WaitP95MS, b.Frames.WaitP95MS},
		{"wait_max_ms", a.Frames.WaitMaxMS, b.Frames.WaitMaxMS},
		{"gui_cpu_seconds", a.GUISeconds, b.GUISeconds},
		{"daemon_cpu_seconds", a.DaemonSeconds, b.DaemonSeconds},
	} {
		if _, err := fmt.Fprintf(w, "%s\t%.3f\t%.3f\t%+.3f\n", row.name, row.a, row.b, row.b-row.a); err != nil {
			return err
		}
	}
	return nil
}
