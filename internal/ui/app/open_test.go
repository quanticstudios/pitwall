package app

import (
	"testing"
)

func TestValidateOpenURL(t *testing.T) {
	for _, good := range []string{
		"https://example.com/path?a=1",
		"http://localhost:8080",
		"HTTP://127.0.0.1:3000",
		"file:///tmp/notes.txt",
		"mailto:user@example.com",
	} {
		if err := validateOpenURL(good); err != nil {
			t.Errorf("validateOpenURL(%q) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{
		"",
		"--help",
		"-a",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"ssh://user@host",
		"smb://host/share",
		"ms-msdt:/id",
		"https://",
		"file://",
		"mailto:",
		"https://example.com/\nfoo",
		"https://example.com/\x00foo",
		"/etc/passwd",
	} {
		if err := validateOpenURL(bad); err == nil {
			t.Errorf("validateOpenURL(%q) = nil, want error", bad)
		}
		if err := OpenURL(bad); err == nil {
			t.Errorf("OpenURL(%q) = nil, want error", bad)
		}
	}
}
