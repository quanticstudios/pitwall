package chatin

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

func TestFixedSettings(t *testing.T) {
	for _, c := range []struct {
		s    *session.Snapshot
		want bool
	}{
		{&session.Snapshot{PaneID: "p", Provider: model.ProviderCodex}, true},
		{&session.Snapshot{Provider: model.ProviderCodex}, false}, // a managed Codex chat
		{&session.Snapshot{PaneID: "p", Provider: model.ProviderClaude}, false},
		{nil, false},
	} {
		if got := fixedSettings(c.s); got != c.want {
			t.Errorf("%+v: %v, want %v", c.s, got, c.want)
		}
	}
}
