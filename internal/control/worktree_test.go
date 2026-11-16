package control

import (
	"fmt"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestOrphans(t *testing.T) {
	st := model.State{Workspaces: []model.Workspace{
		{ID: "a", SessionID: "s"},
		{ID: "b", SessionID: "s", Parent: "a"},                          // its parent is here
		{ID: "c", SessionID: "s", Parent: "gone"},                       // an orphan
		{ID: "d", SessionID: "x", Parent: "gone"},                       // another session's
		{ID: "e", SessionID: "s", Parent: "remote", RemoteParent: true}, // another machine started it
	}}
	var got []string
	for _, w := range Orphans(st, "s") {
		got = append(got, w.ID)
	}
	if fmt.Sprint(got) != "[c]" {
		t.Errorf("Orphans %v, want [c]", got)
	}
}

func TestQueuedTask(t *testing.T) {
	st := model.State{Tasks: []model.Task{{ID: "t1", SessionID: "s"}, {ID: "t2", SessionID: "x"}, {ID: "t3", SessionID: "s"}}}
	tasks := Queued(st, "s")
	for n, want := range map[string]string{"1": "t1", "#2": "t3", "3": "", "0": "", "x": ""} {
		task, err := QueuedTask(tasks, n)
		if task.ID != want || (err == nil) != (want != "") {
			t.Errorf("QueuedTask(%q) = %q, %v; want %q", n, task.ID, err, want)
		}
	}
}

func TestLane(t *testing.T) {
	s := config.Settings{Fleet: config.FleetSettings{On: true, Lanes: []config.LaneSettings{{Name: "small", Preset: "quick", Host: "box"}, {Name: "off"}}}}
	preset, brief, host, err := Lane(s, "small", "", []string{"fix", "it"})
	if preset != "quick" || fmt.Sprint(brief) != "[fix it]" || host != "box" || err != nil {
		t.Errorf("Lane = %q %q %q %v", preset, brief, host, err)
	}
	for _, tc := range []struct {
		lane, preset string
		brief        []string
		on           bool
	}{{"small", "x", []string{"b"}, true}, {"off", "", []string{"b"}, true}, {"small", "", nil, true}, {"small", "", []string{"b"}, false}} {
		s.Fleet.On = tc.on
		if _, _, _, err := Lane(s, tc.lane, tc.preset, tc.brief); err == nil {
			t.Errorf("Lane(%+v) has no error", tc)
		}
	}
}
