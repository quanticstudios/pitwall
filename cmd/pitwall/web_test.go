package main

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/web"
)

func TestRunWeb(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	run := func(args ...string) (int, string) {
		var out, errs strings.Builder
		code := runWeb(args, &out, &errs)
		return code, out.String() + errs.String()
	}
	if code, out := run("token"); code != 2 || !strings.Contains(out, "usage") {
		t.Fatalf("token: %d %q", code, out)
	}
	if code, out := run("token", "--rotate"); code != 0 || !strings.Contains(out, web.Path()) {
		t.Fatalf("rotate: %d %q", code, out)
	}
	first, err := web.Read(web.Path())
	if err != nil || len(first.Token) != 64 {
		t.Fatalf("web.json %+v %v", first, err)
	}
	run("token", "--rotate")
	if f, _ := web.Read(web.Path()); f.Token == first.Token {
		t.Fatal("rotate kept the token")
	}
}
