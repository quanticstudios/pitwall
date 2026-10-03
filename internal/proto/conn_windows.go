package proto

import (
	"os"
	"path/filepath"
)

func runtimeDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "pitwall")
}

// private trusts the directory: it sits in the user's profile, whose ACL
// already keeps other users out, and Windows has no mode bits to check.
func private(string, os.FileInfo) error { return nil }
