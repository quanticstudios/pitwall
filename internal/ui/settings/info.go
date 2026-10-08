package settings

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	fontapi "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"

	"github.com/quanticstudios/pitwall/internal/agent"
)

// hookFile is what one agent config says about pitwall's hooks.
type hookFile struct {
	Agent, Path string
	Cmd         string // the agent's command, such as "claude"
	Have, Want  int    // events running `pitwall hook <agent>`, of those pitwall installs
	Err         string
	Edited      bool // a plugin pitwall wrote whole, edited since; install leaves it
}

// hookStatus reads (never writes) the agents' hook configs under home:
// Claude Code's and Codex's always, Gemini CLI's, OpenCode's and pi's when
// the agent is installed, as `pitwall hooks install` decides.
func hookStatus(home string) []hookFile {
	out := []hookFile{
		checkHooks("Claude Code", filepath.Join(home, ".claude", "settings.json"), agent.ClaudeHooks("pitwall"), " hook claude"),
		checkHooks("Codex", filepath.Join(home, ".codex", "hooks.json"), agent.CodexHooks("pitwall"), " hook codex"),
	}
	if dir := agent.GeminiDir(home); installed("gemini", dir) {
		out = append(out, checkHooks("Gemini CLI", filepath.Join(dir, "settings.json"), agent.GeminiHooks("pitwall"), " hook gemini"))
	}
	if dir := agent.OpenCodeDir(home); installed("opencode", dir) {
		out = append(out, checkPlugin("OpenCode", "opencode", filepath.Join(dir, "plugins", "pitwall.js"), agent.IsOpenCodePlugin))
	}
	if dir := agent.PiDir(home); installed("pi", dir) {
		out = append(out, checkPlugin("pi", "pi", filepath.Join(dir, "extensions", "pitwall.ts"), agent.IsPiExtension))
	}
	return out
}

// HooksInstalled reports, by agent command, whether each agent hookStatus
// reads under home has every hook pitwall installs.
func HooksInstalled(home string) map[string]bool {
	out := map[string]bool{}
	for _, h := range hookStatus(home) {
		out[h.Cmd] = h.Err == "" && !h.Edited && h.Have > 0 && h.Have == h.Want
	}
	return out
}

func installed(name, dir string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	_, err := os.Stat(dir)
	return err == nil
}

// checkPlugin reports a plugin file pitwall writes whole: installed when
// some pitwall binary wrote it and nobody edited it since.
func checkPlugin(name, cmd, path string, is func([]byte) bool) hookFile {
	h := hookFile{Agent: name, Cmd: cmd, Path: path, Want: 1}
	switch data, err := os.ReadFile(path); {
	case os.IsNotExist(err):
	case err != nil:
		h.Err = err.Error()
	case is(data):
		h.Have = 1
	default:
		h.Edited = true
	}
	return h
}

type hookGroups map[string][]struct {
	Hooks []struct {
		Command string `json:"command"`
	} `json:"hooks"`
}

// checkHooks counts the events in generated whose hooks in path include a
// command that runs a pitwall binary with suffix, wherever it lives.
func checkHooks(name, path string, generated []byte, suffix string) hookFile {
	h := hookFile{Agent: name, Cmd: strings.TrimPrefix(suffix, " hook "), Path: path}
	var want hookGroups
	json.Unmarshal(generated, &want)
	h.Want = len(want)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			h.Err = err.Error()
		}
		return h
	}
	var root struct {
		Hooks hookGroups `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		h.Err = "not valid JSON: " + err.Error()
		return h
	}
	for ev := range want {
		found := false
		for _, g := range root.Hooks[ev] {
			for _, c := range g.Hooks {
				cmd := strings.Trim(strings.TrimSuffix(c.Command, suffix), `'"`)
				if strings.HasSuffix(c.Command, suffix) && filepath.Base(cmd) == "pitwall" {
					found = true
				}
			}
		}
		if found {
			h.Have++
		}
	}
	return h
}

// version is pitwall's version: the -X main.version scripts/install.sh
// links in, else the commit Go recorded, else "dev".
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return versionFrom(info.Settings)
}

func versionFrom(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "-ldflags":
			for _, f := range strings.Fields(s.Value) {
				if v, ok := strings.CutPrefix(strings.Trim(f, `'"`), "main.version="); ok && v != "" {
					return v
				}
			}
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	rev = "dev-" + rev[:min(12, len(rev))]
	if dirty {
		rev += "-dirty"
	}
	return rev
}

var (
	famOnce     sync.Once
	famMu       sync.Mutex
	famList     []string
	famNotifier func()
)

// families lists installed font families by their real names, sorted. The
// first call starts the scan in the background and returns nil; done runs
// when the list is ready.
func families(done func()) []string {
	famMu.Lock()
	defer famMu.Unlock()
	if famList != nil {
		return famList
	}
	famNotifier = done
	famOnce.Do(func() {
		go func() {
			l := scanFamilies()
			famMu.Lock()
			famList = l
			n := famNotifier
			famMu.Unlock()
			if n != nil {
				n()
			}
		}()
	})
	return nil
}

// scanFamilies reads fontscan's index (the one the terminal resolves
// glyphs through, so every name it lists loads) and one face per family
// for its display name.
func scanFamilies() []string {
	dir, _ := os.UserCacheDir()
	fps, err := fontscan.SystemFonts(log.New(io.Discard, "", 0), dir)
	if err != nil {
		log.Printf("settings: fonts: %q", err)
	}
	seen := map[string]bool{}
	out := []string{"Geist"} // bundled
	var buf []byte
	for _, fp := range fps {
		if seen[fp.Family] {
			continue
		}
		seen[fp.Family] = true
		name := describe(fp.Location, &buf)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return slices.CompactFunc(out, strings.EqualFold)
}

func describe(l fontscan.Location, buf *[]byte) string {
	f, err := os.Open(l.File)
	if err != nil {
		return ""
	}
	defer f.Close()
	lds, err := ot.NewLoaders(f)
	if err != nil || int(l.Index) >= len(lds) {
		return ""
	}
	var d fontapi.Description
	d, *buf = fontapi.Describe(lds[l.Index], *buf)
	return d.Family
}
