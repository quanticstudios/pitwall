package timelog

import (
	"maps"
	"testing"
	"time"
)

func TestWrappedCommands(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`rtk proxy go test ./internal/daemon -run TestErrorsLogged`, Test},
		{`rtk proxy sh -c 'go test ./...'`, Test},
		{`rtk proxy bash -lc "go vet ./..."`, Lint},
		{`rtk proxy zsh -c 'mise exec -- go build ./cmd/pitwall'`, Build},
		{`rtk proxy dash -c 'npm ci'`, Install},
		{`rtk proxy sh -c 'env -u PITWALL_SOCKET GOFLAGS=-tags=novulkan mise exec -- go test ./...'`, Test},
		{`rtk proxy sh -c 'cd /tmp/repo && go build ./cmd/pitwall && go test ./...'`, Test},
		{`sh -lc 'sleep 30; gh pr checks 123'`, CI},
		{`rtk git status`, Git},
		{`rtk proxy sh -c 'cd /tmp/repo && go test'`, Test},
		{`rtk proxy sh -c 'printf "go test; gh pr checks"'`, Shell},
		{`sh -c 'sleep 30' && go test ./...`, Test},
		{`rtk proxy git diff --stat`, Git},
		{`rtk proxy sh -c 'rg --files internal'`, Shell},
		{`proxy go test ./...`, Shell},
		{`sh -c "bash -c 'go test ./...'"`, Shell},
		{`dash scripts/get_test.sh`, Test},
	} {
		t.Run(tc.line, func(t *testing.T) {
			if got := Command(tc.line); got != tc.want {
				t.Errorf("Command = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSummarizeCapsAgentTime(t *testing.T) {
	for _, end := range []string{Interrupted, PaneClosed} {
		t.Run(end, func(t *testing.T) {
			const hour = int64(time.Hour / time.Millisecond)
			e := Event{T: at(24000), Kind: Turn, Tab: "t", Start: at(0), End: end,
				Ms: 6 * hour, Wait: hour, Model: 2 * hour, Reply: hour,
				Tools: map[string]int64{Test: 2 * hour, Shell: hour}, Slow: []Cmd{{"go test ./...", Test, 2 * hour}}}
			s := Summarize([]Event{e}, at(0), at(25000), time.UTC)
			limit := idleMax.Milliseconds()
			if s.Agent != limit || s.Wait != hour || s.Median != hour+limit || s.P90 != hour+limit || s.Reply != hour {
				t.Errorf("summary %+v", s)
			}
			want := map[string]int64{Model: 240000, Test: 240000, Shell: 120000, Wait: hour}
			if !maps.Equal(s.Parts, want) || s.Days[0].Total != hour+limit || !maps.Equal(s.Days[0].Parts, want) {
				t.Errorf("parts %v, days %+v", s.Parts, s.Days)
			}
			if tab := s.Tabs[0]; tab.Agent != limit || tab.Tools != 360000 || tab.You != 2*hour {
				t.Errorf("tab %+v", tab)
			}
			if s.Sinks[0].Total != 240000 || s.Sinks[0].Median != 240000 {
				t.Errorf("sink %+v", s.Sinks[0])
			}
			if e.Tools[Test] != 2*hour || e.Slow[0].Ms != 2*hour {
				t.Fatal("summary mutated the log")
			}
		})
	}
}

func TestSummarizePreservesLegitimateTurns(t *testing.T) {
	cutoff := time.Date(2026, 10, 31, 19, 0, 39, 0, time.UTC)
	for _, end := range []string{Completed, Failed, Interrupted, PaneClosed, ""} {
		for _, at := range []time.Time{cutoff.Add(-time.Second), cutoff, cutoff.Add(time.Hour)} {
			if at.Before(cutoff) && (end == Interrupted || end == PaneClosed) {
				continue
			}
			t.Run(end+"/"+at.Format(time.RFC3339), func(t *testing.T) {
				e := Event{T: at, Kind: Turn, Tab: "t", End: end, Ms: 2400000, Model: 600000,
					Tools: map[string]int64{Test: 1800000}, Slow: []Cmd{{"go test ./...", Test, 1800000}}}
				s := Summarize([]Event{e}, at.Add(-time.Hour), at.Add(time.Second), time.UTC)
				if s.Agent != e.Ms || s.Median != e.Ms || s.P90 != e.Ms || s.Parts[Model] != e.Model || s.Parts[Test] != e.Tools[Test] || s.Tabs[0].Tools != e.Tools[Test] || s.Sinks[0].Total != e.Slow[0].Ms {
					t.Errorf("legitimate turn clipped: %+v", s)
				}
			})
		}
	}
}

func TestEscapedShellScripts(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`bash -c "cd \"$dir\" && go test ./..."`, Test},
		{`rtk proxy sh -c "echo \"x\"; go build ./..."`, Build},
		{`zsh -lc "cd \"my dir\" && go vet ./..."`, Lint},
		{`dash -c "printf \"go test; gh pr checks\""`, Shell},
		{`sh -c "go test -run 'TestFoo'" ignored`, Test},
	} {
		t.Run(tc.line, func(t *testing.T) {
			if got := Command(tc.line); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestQuotedCommandSubstitutions(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`echo "$(go test ./...)"`, Test},
		{`x="$(go build ./...)"`, Build},
		{"echo \"`make`\"", Build},
		{`echo "$(go test ./...)$(go vet ./...)"`, Test},
		{`echo '$(go test ./...)'`, Shell},
		{`echo "\$(go test ./...)"`, Shell},
		{`echo "\\$(go test ./...)"`, Test},
		{`printf "go test; gh pr checks"`, Shell},
	} {
		t.Run(tc.line, func(t *testing.T) {
			if got := Command(tc.line); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}
