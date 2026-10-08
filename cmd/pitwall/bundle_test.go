package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoginPath runs a fake shell whose rc file prints noise and prepends
// to PATH, as a user's .zshrc would.
func TestLoginPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell")
	}
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	script := "#!/bin/sh\necho welcome back\nPATH=/from/rc:$PATH; export PATH\n[ \"$1\" = -ilc ] && exec /bin/sh -c \"$2\"\n"
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := loginPath(sh), "/from/rc:"+os.Getenv("PATH"); got != want {
		t.Errorf("loginPath = %q, want %q", got, want)
	}
	if got := loginPath(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("loginPath of a missing shell = %q, want empty", got)
	}
}
