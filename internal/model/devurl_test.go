package model

import (
	"strings"
	"testing"
)

func TestDevURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Fix/Login", "fix-login"}, {"..Web__App--", "web-app"}, {"☃", "worktree"},
		{strings.Repeat("a", 80), strings.Repeat("a", 63)}, {"A.B C", "a-b-c"},
	} {
		if got := dnsLabel(tc.in); got != tc.want {
			t.Errorf("dnsLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	w := Workspace{Branch: "fix/login", WorktreeRoot: "/srv/Web App", Ports: PortBlock{3010, 3019}}
	if got := w.DevURL(7778); got != "http://fix-login.web-app.localhost:7778" {
		t.Fatal(got)
	}
	if w.DevURL(0) != "" {
		t.Fatal("disabled proxy has a URL")
	}
	w.Ports = PortBlock{}
	if w.DevURL(7778) != "" {
		t.Fatal("plain tab has a URL")
	}
}
