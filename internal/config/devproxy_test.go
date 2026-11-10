package config

import (
	"path/filepath"
	"testing"
)

func TestDevProxy(t *testing.T) {
	s, probs := LoadFile(filepath.Join(t.TempDir(), "config.toml"))
	if len(probs) != 0 || s.DevProxy.URLPort() != 7778 {
		t.Fatalf("%+v %v", s.DevProxy, probs)
	}
	for _, tc := range []struct {
		config         string
		port, problems int
	}{
		{"enabled = false", 0, 0}, {"port = 8888", 8888, 0}, {"port = 0", 0, 1},
		{"port = 65536", 0, 1}, {"port = 7778.5", 0, 1},
	} {
		s, probs = LoadFile(write(t, t.TempDir(), "config.toml", "[dev_proxy]\n"+tc.config+"\n"))
		if s.DevProxy.URLPort() != tc.port || len(probs) != tc.problems {
			t.Fatalf("%s: %+v %v", tc.config, s.DevProxy, probs)
		}
	}
}
