package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/quanticstudios/pitwall/internal/store"
)

const screenPatternsURL = "https://raw.githubusercontent.com/quanticstudios/pitwall/main/internal/agent/screen_patterns.json"

// UpdateScreenPatterns loads the cache in dir, then checks GitHub at start
// and daily until ctx ends. enabled controls fetching; cached patterns still
// apply offline. Each accepted version replaces the rules for all callers.
func UpdateScreenPatterns(ctx context.Context, dir string, enabled func() bool) {
	runPatternUpdates(ctx, &http.Client{Timeout: 10 * time.Second}, screenPatternsURL,
		filepath.Join(dir, "screen-patterns.json"), enabled, 24*time.Hour)
}

func runPatternUpdates(ctx context.Context, client *http.Client, url, path string, enabled func() bool, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		// Another daemon using this state directory may have downloaded a fix.
		loadPatternCache(path)
		log.Printf("screen patterns: version %d in use", activePatterns.Load().PatternsVersion)
		if enabled() {
			if err := fetchScreenPatterns(ctx, client, url, path); err != nil && ctx.Err() == nil {
				log.Printf("screen patterns update: %q", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func loadPatternCache(path string) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err == nil {
		defer f.Close()
		var data []byte
		data, err = io.ReadAll(io.LimitReader(f, maxPatternBytes+1))
		if err == nil {
			var p *screenPatterns
			p, err = parseScreenPatterns(data)
			if err == nil {
				activatePatterns(p, "cache")
			}
		}
	}
	if err != nil {
		log.Printf("screen patterns cache: %q", err)
	}
}

func fetchScreenPatterns(ctx context.Context, client *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPatternBytes+1))
	if err != nil {
		return err
	}
	p, err := parseScreenPatterns(data)
	if err != nil {
		return err
	}
	if p.PatternsVersion <= activePatterns.Load().PatternsVersion {
		return nil
	}
	if err := store.WriteAtomic(path, data); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	activatePatterns(p, "download")
	return nil
}

func activatePatterns(p *screenPatterns, source string) {
	for {
		old := activePatterns.Load()
		if p.PatternsVersion <= old.PatternsVersion {
			return
		}
		if activePatterns.CompareAndSwap(old, p) {
			log.Printf("screen patterns: version %d in use (%s)", p.PatternsVersion, source)
			return
		}
	}
}
