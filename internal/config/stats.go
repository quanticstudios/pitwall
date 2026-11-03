package config

import (
	"fmt"
	"path"
)

// Stats is [stats]: tabs left out of Timing and Agent errors.
type Stats struct {
	Exclude []string `toml:"exclude" doc:"Leave tabs out of Timing and Agent errors when their name or branch matches a glob. * matches any text except /; use bot/* for branches under bot/. Empty includes every tab. Logs keep every event"`
}

func resolveStats(c Stats) (Stats, []issue) {
	s := Stats{}
	var issues []issue
	for _, glob := range c.Exclude {
		if _, err := path.Match(glob, ""); err != nil {
			issues = append(issues, issue{"stats.exclude", fmt.Sprintf("%q is not a glob: %v", glob, err)})
		} else {
			s.Exclude = append(s.Exclude, glob)
		}
	}
	return s, issues
}

// Excludes reports whether a tab name or branch matches an exclude glob.
func (s Stats) Excludes(name, branch string) bool {
	for _, glob := range s.Exclude {
		for _, value := range []string{name, branch} {
			if value != "" {
				if ok, _ := path.Match(glob, value); ok {
					return true
				}
			}
		}
	}
	return false
}
