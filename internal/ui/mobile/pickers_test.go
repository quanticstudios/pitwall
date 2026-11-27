package mobile

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/remote"
)

// TestChipLists: a tab's own lists win; a Claude tab from a daemon older
// than them takes the hello's; an agent with none shows no chips.
func TestChipLists(t *testing.T) {
	hello, helloE := []string{"opus"}, []string{"high"}
	agy := remote.Tab{Models: []string{"gemini-4-neon"}, Efforts: remote.AgyEfforts, Modes: remote.AgyModes}
	for _, tc := range []struct {
		name string
		tab  remote.Tab
		want [3][]string
	}{
		{"old daemon, claude", remote.Tab{Picker: true}, [3][]string{hello, helloE, nil}},
		{"claude", remote.Tab{Picker: true, Models: remote.Models, Efforts: remote.Efforts, Modes: []string{"default", "plan"}}, [3][]string{remote.Models, remote.Efforts, {"default", "plan"}}},
		{"agy", agy, [3][]string{agy.Models, agy.Efforts, agy.Modes}},
		{"agy before its models are listed", remote.Tab{Efforts: remote.AgyEfforts, Modes: remote.AgyModes}, [3][]string{nil, remote.AgyEfforts, remote.AgyModes}},
		{"codex", remote.Tab{}, [3][]string{}},
	} {
		if got := chipLists(tc.tab, hello, helloE); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	if modeShort("accept-edits") != "Edits" || modeShort("acceptEdits") != "Edits" || modeShort("") != "Mode" || modeShort("auto") != "Auto" {
		t.Error("mode chip words")
	}
}

// TestModePick: a mode pick goes in as a "mode" command once the agent is
// idle, shows on the chip until the tab reports it or pickHold passes.
func TestModePick(t *testing.T) {
	api := &fakeAPI{}
	c := newChat("p1")
	now := time.Unix(1000, 0)
	env := chatEnv{Online: true, Now: now, Tab: remote.Tab{Item: remote.Item{Pane: "p1", State: "working"}, Mode: "default", Modes: remote.AgyModes},
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			done(call(context.Background(), api))
		}}
	c.cmp.choose(&pick{"mode", "plan"})
	c.firePick(env)
	if len(api.commands) != 0 {
		t.Fatalf("fired mid-turn: %v", api.commands)
	}
	env.Tab.State = "completed"
	c.firePick(env)
	if !slices.Equal(api.commands, []string{"p1 /mode plan"}) || c.cmp.modeShown(env.Tab.Mode) != "plan" {
		t.Fatalf("commands %v, chip %q", api.commands, c.cmp.modeShown(env.Tab.Mode))
	}
	c.cmp.settle("", "", "default", now.Add(pickHold-time.Millisecond))
	if c.cmp.modeShown("default") != "plan" {
		t.Error("pick dropped before the tab reported it")
	}
	c.cmp.settle("", "", "default", now.Add(pickHold))
	if c.cmp.modeShown("default") != "default" {
		t.Error("a pick that never applied stayed on the chip")
	}
}
