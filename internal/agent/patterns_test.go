package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func patternData(t *testing.T, change func(*screenPatterns)) []byte {
	t.Helper()
	var p screenPatterns
	if err := json.Unmarshal(embeddedPatterns, &p); err != nil {
		t.Fatal(err)
	}
	change(&p)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func restorePatterns(t *testing.T) {
	t.Helper()
	p := activePatterns.Load()
	t.Cleanup(func() { activePatterns.Store(p) })
}

func TestParseScreenPatterns(t *testing.T) {
	if _, err := parseScreenPatterns(embeddedPatterns); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"malformed":      []byte(`{`),
		"unknown field":  bytes.Replace(embeddedPatterns, []byte(`"common"`), []byte(`"code"`), 1),
		"trailing data":  append(bytes.Clone(embeddedPatterns), []byte(` {}`)...),
		"oversize":       bytes.Repeat([]byte(" "), maxPatternBytes+1),
		"schema":         patternData(t, func(p *screenPatterns) { p.SchemaVersion++ }),
		"zero version":   patternData(t, func(p *screenPatterns) { p.PatternsVersion = 0 }),
		"bad regex":      patternData(t, func(p *screenPatterns) { p.Common.BusyRegex = []string{"["} }),
		"empty matcher":  patternData(t, func(p *screenPatterns) { p.Common.Busy = append(p.Common.Busy, " ") }),
		"long matcher":   patternData(t, func(p *screenPatterns) { p.Common.Busy = []string{strings.Repeat("x", 4097)} }),
		"many matchers":  patternData(t, func(p *screenPatterns) { p.Common.Busy = strings.Fields(strings.Repeat("x ", 257)) }),
		"failed fixture": patternData(t, func(p *screenPatterns) { p.Common.Form = nil }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseScreenPatterns(data); err == nil {
				t.Fatal("accepted invalid screen patterns")
			}
		})
	}
}

func TestScreenChecksCoverFixtures(t *testing.T) {
	data, err := screenFixtures.ReadFile("testdata/screen_checks.json")
	if err != nil {
		t.Fatal(err)
	}
	var checks []struct{ File string }
	if err := json.Unmarshal(data, &checks); err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, c := range checks {
		covered[c.File] = true
	}
	files, err := screenFixtures.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".txt") && !covered[f.Name()] {
			t.Errorf("%s has no bundled self-check", f.Name())
		}
	}
}

func TestFetchScreenPatterns(t *testing.T) {
	restorePatterns(t)
	initial := activePatterns.Load()
	newer := patternData(t, func(p *screenPatterns) {
		p.PatternsVersion = initial.PatternsVersion + 1
		p.Common.Busy = append(p.Common.Busy, "new working hint")
		p.Common.BusyRegex = append(p.Common.BusyRegex, "^new regex hint$", "^other regex hint$")
		gemini := p.Providers[model.ProviderGemini]
		gemini.Busy = append(gemini.Busy, "new provider hint")
		p.Providers[model.ProviderGemini] = gemini
	})
	for _, tc := range []struct {
		name                string
		data                []byte
		code                int
		wantErr, wantUpdate bool
	}{
		{"newer", newer, 200, false, true},
		{"equal", embeddedPatterns, 200, false, false},
		{"older", patternData(t, func(p *screenPatterns) { p.PatternsVersion++ }), 200, false, false},
		{"malformed", []byte(`{`), 200, true, false},
		{"schema", patternData(t, func(p *screenPatterns) { p.SchemaVersion++ }), 200, true, false},
		{"fixture", patternData(t, func(p *screenPatterns) { p.Common.Form = nil }), 200, true, false},
		{"oversize", bytes.Repeat([]byte(" "), maxPatternBytes+1), 200, true, false},
		{"http error", newer, 503, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			activePatterns.Store(initial)
			if tc.name == "older" {
				p, err := parseScreenPatterns(patternData(t, func(p *screenPatterns) { p.PatternsVersion += 2 }))
				if err != nil {
					t.Fatal(err)
				}
				activePatterns.Store(p)
			}
			before := activePatterns.Load()
			path := filepath.Join(t.TempDir(), "screen-patterns.json")
			if err := os.WriteFile(path, embeddedPatterns, 0600); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				w.Write(tc.data)
			}))
			defer srv.Close()
			err := fetchScreenPatterns(t.Context(), srv.Client(), srv.URL, path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fetch = %v, want error %v", err, tc.wantErr)
			}
			want := embeddedPatterns
			if tc.wantUpdate {
				want = newer
				e := vt.New(100, 30, nil)
				e.Write([]byte("new working hint"))
				g := e.Snapshot()
				if ReadScreen(g) != ScreenBusy || State(g) != model.StateWorking || StateOf(model.ProviderCodex, g) != model.StateWorking {
					t.Fatal("download did not change running detection")
				}
				for _, tc := range []struct {
					provider      model.Provider
					text, pattern string
				}{
					{model.ProviderClaude, "new working hint", "new working hint"},
					{model.ProviderClaude, "new regex hint", "^new regex hint$"},
					{model.ProviderClaude, "other regex hint", "^other regex hint$"},
					{model.ProviderGemini, "new provider hint", "new provider hint"},
				} {
					e := vt.New(100, 30, nil)
					e.Write([]byte(tc.text))
					g := e.Snapshot()
					trace := ExplainScreen(tc.provider, g)
					if trace.State != model.StateWorking || trace.State != StateOf(tc.provider, g) || trace.Matched == nil {
						t.Fatalf("%s: wrong trace: %+v", tc.text, trace)
					}
					if trace.Matched.Pattern != tc.pattern || trace.Matched.Line != tc.text {
						t.Fatalf("%s: wrong matching evidence: %+v", tc.text, trace.Matched)
					}
					if tc.text == "other regex hint" && !slices.ContainsFunc(trace.Checks, func(c ScreenCheck) bool {
						return c.Pattern == "^new regex hint$" && !c.Matched
					}) {
						t.Fatal("trace dropped an unsuccessful regex")
					}
				}
			} else if activePatterns.Load() != before {
				t.Fatal("rejected update replaced active patterns")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("cache differs: %v", err)
			}
		})
	}
}

func TestPatternCache(t *testing.T) {
	restorePatterns(t)
	initial := activePatterns.Load()
	path := filepath.Join(t.TempDir(), "screen-patterns.json")
	loadPatternCache(path)
	if activePatterns.Load() != initial {
		t.Fatal("missing cache replaced active patterns")
	}
	newer := patternData(t, func(p *screenPatterns) { p.PatternsVersion++ })
	if err := os.WriteFile(path, newer, 0600); err != nil {
		t.Fatal(err)
	}
	loadPatternCache(path)
	accepted := activePatterns.Load()
	if accepted.PatternsVersion != initial.PatternsVersion+1 {
		t.Fatal("cache was not loaded")
	}
	for _, data := range [][]byte{[]byte(`{`), embeddedPatterns} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		loadPatternCache(path)
		if activePatterns.Load() != accepted {
			t.Fatal("invalid or old cache replaced active patterns")
		}
	}
}

func TestPatternUpdatesRefresh(t *testing.T) {
	restorePatterns(t)
	version := activePatterns.Load().PatternsVersion
	newer := patternData(t, func(p *screenPatterns) { p.PatternsVersion = version + 1 })
	latest := patternData(t, func(p *screenPatterns) { p.PatternsVersion = version + 2 })
	path := filepath.Join(t.TempDir(), "screen-patterns.json")
	var enabled atomic.Bool
	var requests atomic.Int32
	checked := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write(latest)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPatternUpdates(ctx, srv.Client(), srv.URL, path, func() bool {
			select {
			case checked <- struct{}{}:
			default:
			}
			return enabled.Load()
		}, 5*time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("updater did not start")
	}
	if requests.Load() != 0 {
		t.Fatal("fetching ran while disabled")
	}
	// A second daemon downloads a file after this daemon has started.
	if err := os.WriteFile(path, newer, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for activePatterns.Load().PatternsVersion != version+1 {
		select {
		case <-checked:
		case <-deadline:
			t.Fatal("running daemon did not reload another daemon's cache")
		}
	}
	enabled.Store(true)
	deadline = time.After(5 * time.Second)
	for activePatterns.Load().PatternsVersion != version+2 {
		select {
		case <-checked:
		case <-deadline:
			t.Fatal("periodic download did not activate")
		}
	}
	if requests.Load() == 0 {
		t.Fatal("periodic fetch did not run")
	}
}

func TestPatternUpdatesStartAndCancel(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	path := filepath.Join(t.TempDir(), "screen-patterns.json")
	go func() {
		defer close(done)
		runPatternUpdates(ctx, srv.Client(), srv.URL, path, func() bool { return true }, 24*time.Hour)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("updater did not fetch at start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("updater did not cancel its running request")
	}
}

func TestFetchPatternsTimeoutAndCacheFailure(t *testing.T) {
	restorePatterns(t)
	initial := activePatterns.Load()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	client := srv.Client()
	client.Timeout = 20 * time.Millisecond
	path := filepath.Join(t.TempDir(), "screen-patterns.json")
	if err := fetchScreenPatterns(t.Context(), client, srv.URL, path); err == nil {
		t.Fatal("fetch ignored timeout")
	}
	srv.Close()
	newer := patternData(t, func(p *screenPatterns) { p.PatternsVersion++ })
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(newer) }))
	defer srv.Close()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fetchScreenPatterns(t.Context(), srv.Client(), srv.URL, path); err == nil {
		t.Fatal("fetch ignored cache failure")
	}
	if activePatterns.Load() != initial {
		t.Fatal("failed fetch replaced active patterns")
	}
}
