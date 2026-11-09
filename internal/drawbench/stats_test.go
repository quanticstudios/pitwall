package drawbench

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	if got := Summarize(nil); got != (Frames{}) {
		t.Fatal(got)
	}
	samples := []time.Duration{20 * time.Millisecond, time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond}
	got := Summarize(samples)
	if got.Drawn != 4 || got.P50MS != 2 || got.P95MS != 20 || got.MaxMS != 20 {
		t.Fatal(got)
	}
	if samples[0] != 20*time.Millisecond {
		t.Fatal("modified input")
	}
	if got := Summarize([]time.Duration{123 * time.Microsecond}); got.P95MS != .123 {
		t.Fatal(got)
	}
}

func TestCompare(t *testing.T) {
	a := Result{Workload: "spinners", Seconds: 10, Build: "a", Frames: Frames{Drawn: 4, P50MS: 2, P95MS: 3, MaxMS: 4, Scale: 1}, GUISeconds: 2, DaemonSeconds: 1}
	b := a
	b.Build, b.Frames.Drawn, b.Frames.P50MS, b.DaemonSeconds = "b", 6, 1, 2
	var out bytes.Buffer
	if err := Compare(&out, a, b); err != nil {
		t.Fatal(err)
	}
	want := "build\ta\tb\nmetric\ta\tb\tdelta (b-a)\nframes\t4.000\t6.000\t+2.000\np50_ms\t2.000\t1.000\t-1.000\np95_ms\t3.000\t3.000\t+0.000\nmax_ms\t4.000\t4.000\t+0.000\nwait_p50_ms\t0.000\t0.000\t+0.000\nwait_p95_ms\t0.000\t0.000\t+0.000\nwait_max_ms\t0.000\t0.000\t+0.000\ngui_cpu_seconds\t2.000\t2.000\t+0.000\ndaemon_cpu_seconds\t1.000\t2.000\t+1.000\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", &out, want)
	}
	for _, mutate := range []func(*Result){func(r *Result) { r.Workload = "flood" }, func(r *Result) { r.Seconds = 5 }, func(r *Result) { r.Frames.Scale = 2 }, func(r *Result) { r.Frames.Timing = "layout-submit-v1" }, func(r *Result) { r.VSync = "off" }} {
		bad := b
		mutate(&bad)
		if err := Compare(&out, a, bad); err == nil {
			t.Fatal("accepted mismatched settings")
		}
	}
	if err := Compare(failedWriter{}, a, b); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatal(err)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
