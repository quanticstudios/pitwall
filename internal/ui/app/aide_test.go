package app

import (
	"errors"
	"reflect"
	"testing"
)

// TestAideArgv: aide is found as its macOS app or its Linux desktop
// entry, user folders first, and nowhere else.
func TestAideArgv(t *testing.T) {
	env := map[string]string{"HOME": "/h"}
	files := map[string]string{}
	getenv := func(k string) string { return env[k] }
	exists := func(p string) bool { _, ok := files[p]; return ok }
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no file")
	}
	check := func(goos string, want []string) {
		t.Helper()
		if got := aideArgv(goos, getenv, exists, read); !reflect.DeepEqual(got, want) {
			t.Errorf("%s with %v: %q, want %q", goos, files, got, want)
		}
	}
	check("linux", nil)
	check("darwin", nil)

	files["/Applications/aIDE.app"] = ""
	check("darwin", []string{"open", "-a", "/Applications/aIDE.app"})
	files["/h/Applications/aIDE.app"] = ""
	check("darwin", []string{"open", "-a", "/Applications/aIDE.app"})
	check("windows", nil)

	files["/usr/share/applications/aide.desktop"] = "[Desktop Entry]\nName=aIDE\nExec=/opt/aIDE/aide --no-sandbox %U\n[Desktop Action New]\nExec=/other\n"
	check("linux", []string{"/opt/aIDE/aide", "--no-sandbox"})
	files["/h/.local/share/applications/aide.desktop"] = "[Desktop Action X]\nExec=/wrong\n[Desktop Entry]\nExec=/h/Apps/aide.AppImage\n"
	check("linux", []string{"/h/Apps/aide.AppImage"})
	env["XDG_DATA_HOME"], env["XDG_DATA_DIRS"] = "/x", "/y"
	check("linux", nil)
}
