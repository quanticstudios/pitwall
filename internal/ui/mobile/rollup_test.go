package mobile

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
)

func TestBuildRowsFoldsWork(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	es := demoTurn("failed", at)
	closed := func(int64) bool { return false }
	rows, turns := buildRows(nil, nil, es, closed, false)
	// The prompt, the Worked line, the reply, the diff card.
	want := []rowKind{rowEntry, rowWork, rowEntry, rowDiff}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i, k := range want {
		if rows[i].kind != k {
			t.Fatalf("row %d is %d, want %d: %+v", i, rows[i].kind, k, rows)
		}
	}
	if es[rows[2].i].Kind != flow.EntryAssistant || rows[2].i != len(es)-1 {
		t.Errorf("the turn's last reply is not the row after the line: %+v", rows[2])
	}
	tw := turns[0]
	if tw.tools != 8 || tw.failed != 1 || tw.running {
		t.Errorf("tools %d, failed %d, running %v", tw.tools, tw.failed, tw.running)
	}
	if got := tw.title(at); got != "Worked 2m 14s" {
		t.Errorf("title %q", got)
	}
	add, del := tw.totals()
	if len(tw.files) != 2 || add != 18 || del != 4 {
		t.Errorf("files %+v, +%d −%d", tw.files, add, del)
	}
	if f := tw.files[0]; f.name != "login.go" || f.add != 11 || f.del != 3 {
		t.Errorf("login.go's edits add up to %+v", f)
	}

	open := func(int64) bool { return true }
	rows, _ = buildRows(rows, turns, es, open, false)
	if n := len(rows); n != 4+9 { // the 8 calls and the reply between them
		t.Errorf("open fold has %d rows", n)
	}
}

func TestBuildRowsRunningAndFailedEdit(t *testing.T) {
	es := []flow.Entry{
		{Seq: 1, Kind: flow.EntryUser, Text: "go", At: 1000},
		{Seq: 2, Kind: flow.EntryTool, Tool: "Edit", Arg: "a.go", Done: true, Failed: true, Add: 3, At: 2000},
		{Seq: 3, Kind: flow.EntryTool, Tool: "Edit", Arg: "b.go", Done: true, Add: 1, Del: 1, At: 3000},
		{Seq: 4, Kind: flow.EntryTool, Tool: "Bash", Arg: "go test", At: 4000},
	}
	rows, turns := buildRows(nil, nil, es, func(int64) bool { return false }, false)
	if len(rows) != 2 || rows[1].kind != rowWork {
		t.Fatalf("a turn with no reply yet: %+v", rows)
	}
	tw := turns[0]
	if !tw.running || tw.failed != 1 || len(tw.files) != 1 || tw.files[0].name != "b.go" {
		t.Errorf("%+v", tw)
	}
	if got := tw.title(time.UnixMilli(41_000)); got != "Working 40s" {
		t.Errorf("title %q", got)
	}
	// A turn with no work shows its entries as they are.
	rows, _ = buildRows(rows, turns, es[:1], func(int64) bool { return false }, true)
	if len(rows) != 1 || rows[0].kind != rowEntry {
		t.Errorf("%+v", rows)
	}
}

func TestRowOfFoldedEntry(t *testing.T) {
	c := newChat("p1")
	c.data.Entries = demoTurn("failed", time.Unix(1_800_000_000, 0))
	c.buildRows(false)
	if got := c.rowOf(4); got != 1 {
		t.Errorf("a folded call's row is %d, want the Worked line's 1", got)
	}
	if got := c.rowOf(len(c.data.Entries) - 1); got != 2 {
		t.Errorf("the reply's row is %d", got)
	}
}
