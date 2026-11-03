package settings

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/timelog"
)

func TestStatsWindowsExcludeTabs(t *testing.T) {
	now := time.Now()
	stats := config.Stats{Exclude: []string{"watcher*", "bot/*"}}
	var turns []timelog.Event
	var errors []errlog.Event
	for _, tab := range []struct{ id, name, branch string }{
		{"w", "watcher", "main"}, {"b", "dependencies", "bot/update"}, {"f", "feature", "fix-stats"},
	} {
		for range 2 {
			turns = append(turns, timelog.Event{T: now.Add(-time.Hour), Kind: timelog.Turn, Tab: tab.id, Name: tab.name, Branch: tab.branch,
				Ms: 2000, Model: 1000, Tools: map[string]int64{timelog.Test: 1000}, Slow: []timelog.Cmd{{Command: "go test ./...", Kind: timelog.Test, Ms: 1000}}})
			errors = append(errors, errlog.Event{T: now.Add(-time.Hour), Tab: tab.id, Pane: tab.id + "-pane", Name: tab.name, Branch: tab.branch,
				Kind: errlog.Command, Provider: "codex", Command: "go test ./...", Output: "exit status 1", Size: 40})
		}
	}
	// A renamed tab and a second pane still belong to the excluded tab.
	turns = append(turns, timelog.Event{T: now.Add(-time.Minute), Kind: timelog.Turn, Tab: "w", Name: "renamed", Ms: 5000, Model: 5000})
	errors = append(errors, errlog.Event{T: now.Add(-time.Minute), Tab: "w", Pane: "second", Name: "renamed", Kind: errlog.API})
	// A legacy error with no tab metadata still counts.
	errors = append(errors, errlog.Event{T: now.Add(-time.Minute), Pane: "legacy", Kind: errlog.Tool, Size: 8})
	s, any, n := timingWindow(turns, week, now, stats)
	if !any || n != 2 || s.Turns != 2 || s.Agent != 4000 || len(s.Tabs) != 1 || s.Tabs[0].TabName != "feature" || s.Sinks[0].Count != 2 || s.Parts[timelog.Test] != 2000 {
		t.Fatalf("timing %+v, any %v, excluded %d", s, any, n)
	}
	e, n := errorWindow(errors, week, now, stats)
	if n != 2 || e.Total != 3 || e.Tokens != 22 || e.Kinds[errlog.Command] != 2 || e.Providers["codex"] != 2 {
		t.Fatalf("errors %+v, excluded %d", e, n)
	}
	var days, groups int
	for _, d := range e.Days {
		days += d.Count
	}
	for _, g := range e.Groups {
		groups += g.Count
	}
	if days != 3 || groups != 3 {
		t.Fatalf("days %d, groups %d", days, groups)
	}
	all := config.Stats{Exclude: []string{"*"}}
	s, any, n = timingWindow(turns, week, now, all)
	if s.Turns != 0 || !any || n != 3 {
		t.Fatalf("all excluded: %+v, any %v, excluded %d", s, any, n)
	}
	_, _, n = timingWindow(turns, week, now.AddDate(0, 0, 20), stats)
	if n != 0 {
		t.Fatalf("excluded %d tabs outside the window", n)
	}
}

func TestLeaveOutOfStatsSavesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	const source = "# keep my comment\n[stats]\nexclude = [\"bot/*\"]\n[font]\nui_size = 14\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var p Page
	p.Show(path)
	p.s, _ = config.LoadFile(path)
	p.tm.path, p.er.path = filepath.Join(dir, "timing.jsonl"), filepath.Join(dir, "errors.jsonl")
	name := "watcher \"nightly\""
	p.excludeTab(name)
	if p.err != "" || p.result != Saved {
		t.Fatalf("save: %q, %v", p.err, p.result)
	}
	s, probs := config.LoadFile(path)
	if len(probs) != 0 || !slices.Equal(s.Stats.Exclude, []string{"bot/*", name}) {
		t.Fatalf("config %+v, problems %v", s.Stats, probs)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# keep my comment") || !strings.Contains(string(b), "ui_size = 14") {
		t.Fatalf("changed other config: %s", b)
	}
	p.excludeTab(name)
	s, _ = config.LoadFile(path)
	if len(s.Stats.Exclude) != 2 {
		t.Fatalf("duplicate patterns: %v", s.Stats.Exclude)
	}
	// Finish both reads before the temporary directory goes away.
	for start := time.Now(); ; time.Sleep(time.Millisecond) {
		p.tm.mu.Lock()
		timing := p.tm.loading
		p.tm.mu.Unlock()
		p.er.mu.Lock()
		errors := p.er.loading
		p.er.mu.Unlock()
		if !timing && !errors {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("stats read did not finish")
		}
	}
}
