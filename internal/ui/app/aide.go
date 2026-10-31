package app

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// findAide is the command that opens aide on this machine, nil when aide
// is not installed; tests replace it.
//
// shortcut: aide cannot open one chat yet (its bridge is M6), so Open in
// aide opens aide, or focuses it when it runs.
var findAide = sync.OnceValue(func() []string {
	return aideArgv(runtime.GOOS, os.Getenv, func(p string) bool { _, err := os.Stat(p); return err == nil }, os.ReadFile)
})

// aideArgv finds aide as its packages install it: aIDE.app in an
// Applications folder on macOS, an aide.desktop entry in the XDG data
// folders on Linux. aide ships no Windows build.
func aideArgv(goos string, getenv func(string) string, exists func(string) bool, read func(string) ([]byte, error)) []string {
	home := getenv("HOME")
	switch goos {
	case "darwin":
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if app := filepath.Join(dir, "aIDE.app"); exists(app) {
				return []string{"open", "-a", app}
			}
		}
	case "windows":
	default:
		dirs := []string{getenv("XDG_DATA_HOME")}
		if dirs[0] == "" {
			dirs[0] = filepath.Join(home, ".local", "share")
		}
		data := getenv("XDG_DATA_DIRS")
		if data == "" {
			data = "/usr/local/share:/usr/share"
		}
		for _, dir := range append(dirs, filepath.SplitList(data)...) {
			b, err := read(filepath.Join(dir, "applications", "aide.desktop"))
			if err != nil {
				continue
			}
			if argv := desktopExec(string(b)); argv != nil {
				return argv
			}
		}
	}
	return nil
}

// desktopExec is the command of a desktop entry's Exec line, without its
// field codes (%U, %f, ...).
//
// shortcut: words split on spaces; a quoted Exec path with spaces is not
// handled, which no aide package writes.
func desktopExec(entry string) []string {
	section := ""
	for line := range strings.Lines(entry) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		rest, ok := strings.CutPrefix(line, "Exec=")
		if !ok || section != "[Desktop Entry]" {
			continue
		}
		var argv []string
		for _, w := range strings.Fields(rest) {
			if !strings.HasPrefix(w, "%") {
				argv = append(argv, w)
			}
		}
		return argv
	}
	return nil
}

// openAide runs argv without waiting for it.
func openAide(argv []string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		log.Printf("open aide: %q", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
