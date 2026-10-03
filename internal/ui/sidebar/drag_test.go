package sidebar

import "testing"

// rows: two ungrouped sessions, group g1 with a and b, collapsed group g2,
// group g3 with c.
var dragRows = []dropRow{
	{'s', "u1", "", 4, 60},
	{'s', "u2", "", 62, 150}, // with tab rows under it
	{'g', "g1", "g1", 163, 203},
	{'s', "a", "g1", 207, 263},
	{'s', "b", "g1", 265, 321},
	{'g', "g2", "g2", 338, 378},
	{'g', "g3", "g3", 391, 431},
	{'s', "c", "g3", 435, 491},
}

func TestSessionDrop(t *testing.T) {
	for _, tc := range []struct {
		y    int
		want drop
	}{
		{0, drop{group: "", before: "u1", line: 4, ok: true}},
		{40, drop{group: "", before: "u2", line: 60, ok: true}},
		{61, drop{group: "", before: "u2", line: 62, ok: true}}, // the 2px gap goes to the nearer row
		{140, drop{group: "", line: 150, ok: true}},              // last of the ungrouped
		{170, drop{group: "g1", into: true, ok: true}},           // onto a header
		{210, drop{group: "g1", before: "a", line: 207, ok: true}},
		{300, drop{group: "g1", line: 321, ok: true}},
		{350, drop{group: "g2", into: true, ok: true}}, // a collapsed group
		{470, drop{group: "g3", line: 491, ok: true}},
		{900, drop{group: "g3", line: 491, ok: true}}, // below everything
	} {
		if got := sessionDrop(dragRows, tc.y); got != tc.want {
			t.Errorf("y=%d: %+v, want %+v", tc.y, got, tc.want)
		}
	}
	if d := sessionDrop(nil, 10); d.ok {
		t.Error("drop with no rows")
	}
}

func TestGroupDrop(t *testing.T) {
	for _, tc := range []struct {
		y    int
		want drop
	}{
		{20, drop{before: "g1", line: 163, ok: true}},  // over the ungrouped sessions: first
		{200, drop{before: "g1", line: 163, ok: true}}, // top half of g1's block
		{300, drop{before: "g2", line: 321, ok: true}}, // bottom half: after g1
		{360, drop{before: "g3", line: 378, ok: true}},
		{480, drop{line: 491, ok: true}}, // last
	} {
		if got := groupDrop(dragRows, tc.y); got != tc.want {
			t.Errorf("y=%d: %+v, want %+v", tc.y, got, tc.want)
		}
	}
}
