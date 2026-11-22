package codex

import (
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Codex 0.171.0's menu also takes /quit for /exit and /btw for /side,
// and lists /apps only for an account with apps; names stay unique
// across names and aliases, so a typed alias finds one command.
func TestBuiltins(t *testing.T) {
	find := func(name string) (session.Command, bool) {
		i := slices.IndexFunc(Builtins, func(c session.Command) bool { return c.Is(name) })
		if i < 0 {
			return session.Command{}, false
		}
		return Builtins[i], true
	}
	for alias, name := range map[string]string{"quit": "exit", "btw": "side"} {
		if c, ok := find(alias); !ok || c.Name != name {
			t.Errorf("/%s finds %+v, want /%s", alias, c, name)
		}
	}
	if c, ok := find("apps"); !ok || !c.Terminal {
		t.Errorf("/apps %+v, want a terminal command", c)
	}
	seen := map[string]bool{}
	for _, c := range Builtins {
		for _, n := range append([]string{c.Name}, c.Aliases...) {
			if seen[n] {
				t.Errorf("/%s listed twice", n)
			}
			seen[n] = true
		}
	}
}
