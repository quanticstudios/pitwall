package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// testdata/session-deltas is aide's fixtures/session-deltas, copied from
// aide a265313 (ENG-2666, which added items' createdAt), and tool-calls.json
// from 1942ada (ENG-2665, which added tools' startedAt and endedAt).
// shortcut: aide's provider-error.json and usage-limit.json (ENG-2670) need
// delta fields pitwall lacks; copy them once Delta has those.
type fixture struct {
	Name          string            `json:"-"`
	RuntimeEvents []json.RawMessage `json:"runtimeEvents"`
	Deltas        []json.RawMessage `json:"deltas"`
	Snapshot      json.RawMessage   `json:"snapshot"`
}

func loadFixtures(t testing.TB) []fixture {
	t.Helper()
	paths, _ := filepath.Glob("testdata/session-deltas/*.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	var out []fixture
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f fixture
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		f.Name = filepath.Base(p)
		out = append(out, f)
	}
	return out
}

func decode[T any](t testing.TB, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// jsonValue is b as a generic JSON value, minus the keys in drop.
func jsonValue(t testing.TB, b []byte, drop func(string) bool) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if m, ok := v.(map[string]any); ok && drop != nil {
		for k := range m {
			if drop(k) {
				delete(m, k)
			}
		}
	}
	return v
}

func sameJSON(t testing.TB, what string, got, want []byte, drop func(string) bool) {
	t.Helper()
	if !reflect.DeepEqual(jsonValue(t, got, drop), jsonValue(t, want, drop)) {
		t.Errorf("%s:\ngot  %s\nwant %s", what, got, want)
	}
}

func TestFixturesRoundTrip(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			for _, raw := range f.Deltas {
				b, err := json.Marshal(decode[Delta](t, raw))
				if err != nil {
					t.Fatal(err)
				}
				sameJSON(t, "delta", b, raw, nil)
			}
			for _, raw := range f.RuntimeEvents {
				b, _ := json.Marshal(decode[Event](t, raw))
				sameJSON(t, "event", b, raw, nil)
			}
			b, _ := json.Marshal(decode[Snapshot](t, f.Snapshot))
			sameJSON(t, "snapshot", b, f.Snapshot, nil)
		})
	}
}

func TestFixturesApply(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			var s *Snapshot
			var seq int64
			for _, raw := range f.Deltas {
				d := decode[Delta](t, raw)
				n, err := Apply(s, seq, d)
				if err != nil {
					t.Fatalf("seq %d: %v", d.Seq, err)
				}
				s, seq = &n, d.Seq
			}
			b, _ := json.Marshal(s)
			sameJSON(t, "snapshot", b, f.Snapshot, nil)
		})
	}
}

// TestFixturesReduce is aide's parity check: the loop's send-side deltas
// (the ones stamped before the first runtime event), then Reduce over the
// runtime events, end on the fixture's snapshot. Revision and the keys
// pitwall has no field for are left out: aide's Jev publishes its own
// revisions and fleet state.
func TestFixturesReduce(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			events := make([]Event, len(f.RuntimeEvents))
			for i, raw := range f.RuntimeEvents {
				events[i] = decode[Event](t, raw)
			}
			var s *Snapshot
			var seq int64
			for _, raw := range f.Deltas {
				d := decode[Delta](t, raw)
				if d.UpdatedAt >= events[0].CreatedAt {
					break
				}
				n, err := Apply(s, seq, d)
				if err != nil {
					t.Fatal(err)
				}
				s, seq = &n, d.Seq
			}
			for _, e := range events {
				rev := s.Revision + 1
				for _, d := range Reduce(*s, e) {
					seq++
					d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = DeltaVersion, s.SessionID, seq, rev, e.CreatedAt
					// Through JSON, as the log and the socket carry it.
					b, err := json.Marshal(d)
					if err != nil {
						t.Fatal(err)
					}
					n, err := Apply(s, seq-1, decode[Delta](t, b))
					if err != nil {
						t.Fatalf("%s: %s: %v", e.Type, b, err)
					}
					s = &n
				}
			}
			b, _ := json.Marshal(s)
			sameJSON(t, "snapshot", b, f.Snapshot, func(k string) bool { return k == "revision" || !snapshotKeys[k] })
		})
	}
}

// FuzzApplyShuffled applies a fixture's deltas in a shuffled order: Apply
// never panics, takes each delta that follows the last one applied, and
// reports every other as a gap.
func FuzzApplyShuffled(f *testing.F) {
	fixtures := loadFixtures(f)
	for i := range uint64(4) {
		f.Add(i, i*7+1)
	}
	f.Fuzz(func(t *testing.T, a, b uint64) {
		r := rand.New(rand.NewPCG(a, b))
		for _, fx := range fixtures {
			ds := make([]Delta, len(fx.Deltas))
			for i, raw := range fx.Deltas {
				ds[i] = decode[Delta](t, raw)
			}
			r.Shuffle(len(ds), func(i, j int) { ds[i], ds[j] = ds[j], ds[i] })
			var s *Snapshot
			var seq int64
			for _, d := range ds {
				n, err := Apply(s, seq, d)
				var re *ResyncError
				switch {
				case d.Type == DeltaSnapshot || s != nil && d.Seq == seq+1:
					if err != nil {
						t.Fatalf("%s: seq %d after %d: %v", fx.Name, d.Seq, seq, err)
					}
					s, seq = &n, d.Seq
				case s == nil:
					if !errors.As(err, &re) || re.Reason != "no-base" {
						t.Fatalf("%s: seq %d with no base: %v", fx.Name, d.Seq, err)
					}
				default:
					if !errors.As(err, &re) || re.Reason != "seq-gap" {
						t.Fatalf("%s: seq %d after %d: %v, want a seq-gap", fx.Name, d.Seq, seq, err)
					}
				}
			}
		}
	})
}

// FuzzApplyDelta feeds Apply arbitrary JSON as a delta, on no base and on
// each fixture's last snapshot: it returns an error or a snapshot, and
// never panics or changes its base.
func FuzzApplyDelta(f *testing.F) {
	fixtures := loadFixtures(f)
	for _, fx := range fixtures {
		for _, raw := range fx.Deltas {
			f.Add([]byte(raw))
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var d Delta
		if json.Unmarshal(b, &d) != nil {
			return
		}
		_, _ = Apply(nil, 0, d)
		for _, fx := range fixtures {
			base := decode[Snapshot](t, fx.Snapshot)
			base.SessionID = d.SessionID
			before, _ := json.Marshal(base)
			_, _ = Apply(&base, d.Seq-1, d)
			after, _ := json.Marshal(base)
			if !bytes.Equal(before, after) {
				t.Fatalf("Apply changed its base:\n%s\n%s", before, after)
			}
		}
	})
}
