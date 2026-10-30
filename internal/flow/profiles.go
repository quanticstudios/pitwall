package flow

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

// ClaudeLimitsPath separates statusline reports by profile. Hashing keeps
// arbitrary TOML names out of paths. The default keeps its existing file.
func ClaudeLimitsPath(stateDir, profile string) string {
	if profile == "" {
		return filepath.Join(stateDir, ClaudeLimitsFile)
	}
	return filepath.Join(stateDir, fmt.Sprintf("claude-limits-%x.json", sha256.Sum256([]byte(profile))))
}

// ProfileScanner keeps each account's rollout cache separate. Call Read
// serially; Scanner itself protects the files it reads.
type ProfileScanner struct{ scans map[string]*Scanner }

// Read reads the default account and each configured profile independently.
// An account without CodexHome has no separate Codex rollout directory.
func (s *ProfileScanner) Read(home, stateDir string, profiles map[string]config.Profile, now time.Time, days int) []Limit {
	if s.scans == nil {
		s.scans = map[string]*Scanner{}
	}
	names := []string{""}
	for name := range profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	for name := range s.scans {
		if name != "" {
			if _, ok := profiles[name]; !ok {
				delete(s.scans, name)
			}
		}
	}
	var out []Limit
	for _, name := range names {
		p := profiles[name]
		if s.scans[name] == nil {
			s.scans[name] = &Scanner{}
		}
		scan := s.scans[name]
		var srcs []Source
		if name == "" {
			srcs = CodexDays(home, now, days)
		} else if p.CodexHome != "" {
			for i := range days {
				srcs = append(srcs, Source{model.ProviderCodex, filepath.Join(p.CodexHome, "sessions", now.AddDate(0, 0, -i).Format("2006/01/02"))})
			}
		}
		scan.Scan(srcs, now.AddDate(0, 0, -days))
		for _, l := range scan.profileLimits(stateDir, name) {
			l.Profile, l.ProfileLabel = name, p.Label
			out = append(out, l)
		}
	}
	return out
}
