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
	w := Workspace{Branch: "fix/login", Path: "/srv/Web App/.worktrees/fix-login", WorktreeRoot: "/srv/Web App", Ports: PortBlock{3010, 3019}}
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

func TestDevURLSurvivesBranchChanges(t *testing.T) {
	w := Workspace{Path: "/srv/web-app/.worktrees/worker-3", Branch: "worker-3", WorktreeRoot: "/srv/web-app", Ports: PortBlock{3010, 3019}}
	want := w.DevURL(7778)
	for _, branch := range []string{"fix/login", "", "main"} {
		w.Branch = branch
		if got := w.DevURL(7778); got != want {
			t.Fatalf("branch %q: URL %q, want %q", branch, got, want)
		}
	}
}
