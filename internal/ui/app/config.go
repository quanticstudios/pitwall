package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// loaded is a config read off the UI goroutine, ready to apply.
type loaded struct {
	s     config.Settings
	th    *theme.Theme
	probs []string
}

func loadConfig() loaded {
	s, probs := config.Load()
	th, err := theme.New(s.Theme, s.Font)
	l := loaded{s: s, th: th}
	for _, p := range probs {
		l.probs = append(l.probs, p.String())
	}
	if err != nil {
		for _, m := range strings.Split(err.Error(), "\n") {
			l.probs = append(l.probs, "config.toml: "+m)
		}
	}
	return l
}

// apply switches the window to l's keys, theme and spacing.
func (u *ui) apply(l loaded) {
	u.cfg, u.th, u.nav.keys, u.probs = l.s, l.th, l.s.Keys, l.probs
	u.updates.on.Store(l.s.CheckUpdates)
	if u.notifications != nil {
		u.notifications.setRules(l.s.Notifications)
	}
}

// reportProblems shows config problems as one desktop notification, once
// per distinct set. The window keeps running on the defaults for them.
func reportProblems(probs []string, last *string) {
	s := strings.Join(probs, "\n")
	if s == *last {
		return
	}
	*last = s
	if s == "" {
		return
	}
	log.Printf("config: %q", probs)
	body := probs
	if len(body) > 8 {
		body = append(body[:8:8], fmt.Sprintf("… and %d more (pitwall config check)", len(probs)-8))
	}
	go desktopCommand(context.Background(), false, "config", "pitwall config has problems", strings.Join(body, "\n")).Run()
}

// refreshSchemas rewrites the schema files when they differ from this
// build's, so editors validate against the keys this pitwall knows. It
// writes nothing for a user who has neither a config nor a schema.
func refreshSchemas() {
	_, errC := os.Stat(config.Path())
	_, errS := os.Stat(config.SchemaPath())
	if errC != nil && errS != nil {
		return
	}
	for path, data := range map[string][]byte{config.SchemaPath(): config.Schema(), config.ThemeSchemaPath(): config.ThemeSchema()} {
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := writeAtomic(path, data); err != nil {
			log.Printf("schema: %q", err)
		}
	}
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// watchConfig polls the files files returns once a second and calls
// reload when any changed, appeared or went away.
func watchConfig(stop <-chan struct{}, files func() []string, reload func()) {
	stamp := func() string {
		var b strings.Builder
		for _, f := range files() {
			if fi, err := os.Stat(f); err == nil {
				fmt.Fprint(&b, fi.ModTime().UnixNano(), fi.Size())
			}
			b.WriteByte('|')
		}
		return b.String()
	}
	last := stamp()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if s := stamp(); s != last {
				last = s
				reload()
			}
		}
	}
}

// guiState is what the window remembers between runs, outside config.toml.
type guiState struct {
	SidebarHidden bool `json:"sidebar_hidden"`
	// Welcome keeps the first-run card up, across restarts, until the user
	// dismisses it or does anything else.
	Welcome bool `json:"welcome,omitempty"`
}

func guiStatePath() string { return filepath.Join(config.StateDir(), "gui.json") }

func loadGUIState() guiState {
	var g guiState
	data, err := os.ReadFile(guiStatePath())
	if err == nil {
		err = json.Unmarshal(data, &g)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("%q: %q", guiStatePath(), err)
	}
	return g
}

func saveGUIState(g guiState) {
	data, _ := json.Marshal(g)
	if err := writeAtomic(guiStatePath(), append(data, '\n')); err != nil {
		log.Printf("%q", err)
	}
}
