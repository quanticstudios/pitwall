package agent

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Adapted from tuios (MIT): internal/harness/manifests/claude-code.toml
// The busy_regex expression matches Claude's spinner, which has an ellipsis.
//
//go:embed screen_patterns.json
var embeddedPatterns []byte

//go:embed testdata/*.txt testdata/screen_checks.json
var screenFixtures embed.FS

const maxPatternBytes = 256 * 1024

type screenText struct {
	Plan     []string `json:"plan"`
	Input    []string `json:"input"`
	Approval []string `json:"approval"`
	Busy     []string `json:"busy"`
}

type screenPatterns struct {
	SchemaVersion   int    `json:"schema_version"`
	PatternsVersion uint64 `json:"patterns_version"`
	Common          struct {
		Form      []string `json:"form"`
		Busy      []string `json:"busy"`
		BusyRegex []string `json:"busy_regex"`
		Plan      []string `json:"plan"`
		Input     []string `json:"input"`
	} `json:"common"`
	Providers map[model.Provider]screenText `json:"providers"`
	spinner   []*regexp.Regexp
}

var activePatterns atomic.Pointer[screenPatterns]

func init() {
	p, err := parseScreenPatterns(embeddedPatterns)
	if err != nil {
		panic(fmt.Sprintf("embedded screen patterns: %v", err))
	}
	activePatterns.Store(p)
}

func parseScreenPatterns(data []byte) (*screenPatterns, error) {
	if len(data) > maxPatternBytes {
		return nil, fmt.Errorf("screen patterns exceed %d bytes", maxPatternBytes)
	}
	var p screenPatterns
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("screen patterns contain trailing data")
	}
	if p.SchemaVersion != 1 || p.PatternsVersion == 0 {
		return nil, fmt.Errorf("unsupported schema %d or invalid patterns version %d", p.SchemaVersion, p.PatternsVersion)
	}
	lists := [][]string{p.Common.Form, p.Common.Busy, p.Common.Plan, p.Common.Input, p.Common.BusyRegex}
	for _, s := range p.Providers {
		lists = append(lists, s.Plan, s.Input, s.Approval, s.Busy)
	}
	count := 0
	for _, list := range lists {
		count += len(list)
		for _, s := range list {
			if strings.TrimSpace(s) == "" || len(s) > 4096 {
				return nil, fmt.Errorf("patterns must contain 1 to 4096 bytes of text")
			}
		}
	}
	if count > 256 {
		return nil, fmt.Errorf("screen patterns exceed 256 matchers")
	}
	for _, s := range p.Common.BusyRegex {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("busy regex: %w", err)
		}
		p.spinner = append(p.spinner, re)
	}
	if err := p.checkFixtures(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *screenPatterns) checkFixtures() error {
	var checks []struct {
		File     string           `json:"file"`
		Provider model.Provider   `json:"provider"`
		Screen   *Screen          `json:"screen"`
		State    model.AgentState `json:"state"`
	}
	data, err := screenFixtures.ReadFile("testdata/screen_checks.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &checks); err != nil {
		return err
	}
	for _, c := range checks {
		data, err := screenFixtures.ReadFile("testdata/" + c.File)
		if err != nil {
			return err
		}
		e := vt.New(100, 30, nil)
		e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(data), "\n"), "\n", "\r\n")))
		g := e.Snapshot()
		if c.Screen != nil && p.readScreen(g, nil) != *c.Screen {
			return fmt.Errorf("self-check %s: screen is %d, want %d", c.File, p.readScreen(g, nil), *c.Screen)
		}
		if got := p.stateOf(c.Provider, g, nil); got != c.State {
			return fmt.Errorf("self-check %s for %s: state is %q, want %q", c.File, c.Provider, got, c.State)
		}
	}
	return nil
}
