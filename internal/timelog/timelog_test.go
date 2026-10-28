package timelog

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct{ tool, cmd, want string }{
		{"Bash", "go test ./...", Test},
		{"Bash", "env -u PITWALL_SOCKET GOFLAGS=-tags=novulkan go test ./internal/daemon", Test},
		{"Bash", "cd web && npm run test:unit", Test},
		{"Bash", "go build ./cmd/pitwall && go test ./...", Test},
		{"Bash", "sh scripts/get_test.sh", Test},
		{"Bash", "python -m pytest -q", Test},
		{"Bash", "mise exec -- go build ./cmd/pitwall", Build},
		{"Bash", "make", Build},
		{"Bash", "npm run build", Build},
		{"Bash", "gofmt -l internal", Lint},
		{"Bash", "go vet ./...", Lint},
		{"Bash", "npx tsc --noEmit", Lint},
		{"Bash", "git status && git diff", Git},
		{"Bash", "gh pr checks 175 --watch", CI},
		{"Bash", "sleep 60; gh run view 123", CI},
		{"Bash", "sleep 30", CI},
		{"Bash", "until curl -s localhost:8080; do sleep 2; done", CI},
		{"Bash", "git push && gh pr create --fill", CI},
		{"Bash", "npm ci", Install},
		{"Bash", "uv sync", Install},
		{"Bash", "go mod tidy", Install},
		{"Bash", "ls -la", Shell},
		{"Bash", "bash -c 'go test ./x'", Test},
		{"run_shell_command", "cargo clippy", Lint},
		{"bash", "", Shell},
		{"Read", "", Search},
		{"Grep", "", Search},
		{"Glob", "", Search},
		{"Edit", "", Edit},
		{"Write", "", Edit},
		{"NotebookEdit", "", Edit},
		{"Task", "", Agent},
		{"Agent", "", Agent},
		{"WebFetch", "", Web},
		{"WebSearch", "", Web},
		{"mcp__linear__get_issue", "", MCP},
		{"TodoWrite", "", Tool},
	} {
		if got := Classify(tc.tool, tc.cmd); got != tc.want {
			t.Errorf("Classify(%q, %q) = %q, want %q", tc.tool, tc.cmd, got, tc.want)
		}
	}
}

var t0 = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

func TestSplit(t *testing.T) {
	// 0-100s turn. A test 10-40s and a read 30-50s overlap for 10s, which
	// they share; a wait 60-70s sits inside an edit 55-80s.
	calls := []call{
		{span: span{at(10), at(40)}, kind: Test},
		{span: span{at(20), at(30)}, kind: Test}, // under the first: counted once
		{span: span{at(30), at(50)}, kind: Search},
		{span: span{at(55), at(80)}, kind: Edit},
	}
	wall, mdl, wait, tools := split(at(0), at(100), []span{{at(60), at(70)}}, calls)
	want := map[string]int64{Test: 25000, Search: 15000, Edit: 15000}
	if wall != 100000 || wait != 10000 || !maps.Equal(tools, want) || mdl != 35000 {
		t.Fatalf("wall %d model %d wait %d tools %v", wall, mdl, wait, tools)
	}
	if sum := mdl + wait + tools[Test] + tools[Search] + tools[Edit]; sum != wall {
		t.Errorf("parts add to %d, want %d", sum, wall)
	}
}

func where() Where {
	return Where{Pane: "p", Tab: "t", Name: "tab", Branch: "fix-x", Provider: "claude"}
}

// TestTurn: a turn with parallel calls matched by id, an approval wait
// and a slow command, ended by Stop; the next turn reports the reply.
func TestTurn(t *testing.T) {
	var tr Tracker
	w := where()
	if evs := tr.State(w, model.StateWorking, at(0)); len(evs) != 1 || evs[0].Kind != Prompt || evs[0].Tab != "t" {
		t.Fatalf("first prompt %+v", evs)
	}
	tr.Tool("p", agent.Call{ID: "a", Tool: "Bash", Command: "go test ./..."}, at(10))
	tr.Tool("p", agent.Call{ID: "b", Tool: "Read"}, at(12))
	tr.Tool("p", agent.Call{End: true, ID: "b"}, at(14))
	tr.Tool("p", agent.Call{End: true, ID: "a"}, at(40))
	tr.Tool("p", agent.Call{ID: "c", Tool: "Bash", Command: "git push"}, at(50))
	tr.State(w, model.StatePendingApproval, at(51))
	tr.State(w, model.StateWorking, at(71))
	tr.Tool("p", agent.Call{End: true, ID: "c"}, at(73))
	evs := tr.State(w, model.StateCompleted, at(90))
	if len(evs) != 1 {
		t.Fatalf("events %+v", evs)
	}
	e := evs[0]
	if e.Kind != Turn || e.End != Completed || e.Ms != 90000 || e.Wait != 20000 || e.Reply != 0 || e.Branch != "fix-x" {
		t.Errorf("turn %+v", e)
	}
	// 12-14s the test and the read share; git ran 50-51 and 71-73.
	if want := map[string]int64{Test: 29000, Search: 1000, Git: 3000}; !maps.Equal(e.Tools, want) || e.Model != 90000-20000-33000 {
		t.Errorf("tools %v model %d", e.Tools, e.Model)
	}
	if len(e.Slow) != 2 || e.Slow[0] != (Cmd{"go test ./...", Test, 30000}) || e.Slow[1] != (Cmd{"git push", Git, 3000}) {
		t.Errorf("slow %+v", e.Slow)
	}
	evs = tr.State(w, model.StateWorking, at(150))
	if len(evs) != 0 {
		t.Fatalf("second prompt logged %+v", evs)
	}
	if evs = tr.State(w, model.StateCompleted, at(160)); evs[0].Reply != 60000 {
		t.Errorf("reply %+v", evs[0])
	}
}

// TestTurnInterrupted: a call with no end, from an agent that sends no
// ids, runs until the next call; the last one until the user stops the
// turn.
func TestTurnInterrupted(t *testing.T) {
	var tr Tracker
	w := where()
	tr.State(w, model.StateWorking, at(0))
	tr.Tool("p", agent.Call{Tool: "read"}, at(5))
	tr.Tool("p", agent.Call{Tool: "bash", Command: "npm test"}, at(10))
	tr.Tool("p", agent.Call{Tool: "edit"}, at(20))
	evs := tr.State(w, "", at(30))
	e := evs[0]
	if e.End != Interrupted || !maps.Equal(e.Tools, map[string]int64{Search: 5000, Test: 10000, Edit: 10000}) || e.Model != 5000 {
		t.Errorf("turn %+v", e)
	}
	if tr.Has("p") {
		t.Error("turn still open")
	}
}

// TestTurnPreNoPost: a call with an id and no end counts for nothing in
// a turn that finishes, and runs to the end of one the pane's closing
// stops; an end with no start but a run time counts that time.
func TestTurnPreNoPost(t *testing.T) {
	var tr Tracker
	w := where()
	tr.State(w, model.StateWorking, at(0))
	tr.Tool("p", agent.Call{ID: "x", Tool: "Task"}, at(10))
	tr.Tool("p", agent.Call{End: true, Tool: "Shell", Command: "make build", Ms: 4000}, at(30))
	e := tr.State(w, model.StateError, at(40))[0]
	if e.End != Failed || !maps.Equal(e.Tools, map[string]int64{Build: 4000}) || e.Model != 36000 {
		t.Errorf("turn %+v", e)
	}
	if e, ok := tr.Close("p", at(50)); ok {
		t.Errorf("closed a finished turn %+v", e)
	}
	tr.State(w, model.StateWorking, at(60))
	tr.Tool("p", agent.Call{ID: "y", Tool: "Task"}, at(62))
	if e, ok := tr.Close("p", at(70)); !ok || e.End != PaneClosed || e.Ms != 10000 || e.Tools[Agent] != 8000 {
		t.Errorf("close %+v", e)
	}
}

// replay runs calls through a turn from 0s to end s, which ends it as
// completed.
func replay(t *testing.T, end int, calls ...func(*Tracker)) Event {
	t.Helper()
	var tr Tracker
	w := where()
	tr.State(w, model.StateWorking, at(0))
	for _, c := range calls {
		c(&tr)
	}
	evs := tr.State(w, model.StateCompleted, at(end))
	if len(evs) != 1 {
		t.Fatalf("events %+v", evs)
	}
	e := evs[0]
	var sum int64
	for _, v := range e.Tools {
		sum += v
	}
	if e.Model+e.Wait+sum != e.Ms {
		t.Errorf("parts %d+%d+%d add up to %d, not %d", e.Model, e.Wait, sum, e.Model+e.Wait+sum, e.Ms)
	}
	return e
}

func pre(s int, id, tool, cmd string) func(*Tracker) {
	return func(tr *Tracker) { tr.Tool("p", agent.Call{ID: id, Tool: tool, Command: cmd}, at(s)) }
}

func post(s int, id string) func(*Tracker) {
	return func(tr *Tracker) { tr.Tool("p", agent.Call{End: true, ID: id}, at(s)) }
}

// TestTurnLostEnds replays turns from real logs in which calls sent no
// end: before, each ran until the turn ended and was kept as a slow
// command.
func TestTurnLostEnds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		end   int
		calls []func(*Tracker)
		tools map[string]int64
		slow  []Cmd
	}{{
		// Codex, 81 min: other hooks blocked two pull request creates,
		// two deploys and a script, which then ran 54 to 79 min each.
		name: "codex blocked calls",
		end:  4860,
		calls: []func(*Tracker){
			pre(60, "exec-1", "Bash", "gh pr create --fill"),
			pre(70, "exec-2", "Bash", "gh pr create --fill --draft"),
			pre(100, "exec-3", "Bash", "make build"),
			pre(600, "exec-4", "Bash", "deployctl up --service api"),
			pre(645, "exec-5", "Bash", "deployctl up --service web"),
			post(1200, "exec-3"),
			pre(1300, "exec-6", "Bash", "git status"), // the model ran: 1-2 and 4-5 are over
			post(1301, "exec-6"),
			pre(1600, "exec-7", "Bash", "python3 - <<'PY'"),
			pre(2000, "exec-8", "Bash", "go test ./..."),
			post(3000, "exec-8"),
			pre(3100, "exec-9", "Bash", "gh pr checks 7 --watch"), // and 7
			post(4700, "exec-9"),
		},
		tools: map[string]int64{Build: 1100000, Git: 1000, Test: 1000000, CI: 1600000},
		slow: []Cmd{{"gh pr checks 7 --watch", CI, 1600000}, {"make build", Build, 1100000},
			{"go test ./...", Test, 1000000}, {"git status", Git, 1000}},
	}, {
		// Codex, 24 min: a test still running when Codex handed back its
		// output, a patch that failed and two blocked pull request creates.
		name: "codex yielded and failed calls",
		end:  1440,
		calls: []func(*Tracker){
			pre(1, "exec-1", "Bash", "go test ./..."),
			pre(15, "exec-2", "apply_patch", "*** Begin Patch"),
			pre(20, "exec-3", "Bash", "git diff"),
			post(21, "exec-3"),
			pre(480, "exec-4", "Bash", "gh pr create --fill"),
			pre(540, "exec-5", "Bash", "gh pr create --fill"),
			pre(600, "exec-6", "Bash", "make build"),
			post(660, "exec-6"),
		},
		tools: map[string]int64{Git: 1000, Build: 60000},
		slow:  []Cmd{{"make build", Build, 60000}, {"git diff", Git, 1000}},
	}, {
		// Claude Code, 32 min: a failed command's parallel siblings were
		// cancelled, and other hooks blocked two greps, a `true` and an
		// Edit, which ran 11 to 20 min each.
		name: "claude cancelled and blocked calls",
		end:  1920,
		calls: []func(*Tracker){
			pre(100, "toolu_1", "Bash", "go vet ./..."),
			pre(100, "toolu_2", "Bash", "grep -rn Foo ."),
			pre(100, "toolu_3", "Read", ""),
			post(101, "toolu_1"),
			pre(150, "toolu_4", "Read", ""), // the siblings were cancelled
			post(151, "toolu_4"),
			pre(732, "toolu_5", "Bash", `grep -n "Limit = " -A3 internal/x.go`),
			pre(740, "toolu_6", "Bash", "go test ./..."),
			pre(780, "toolu_7", "Bash", "true"),
			pre(798, "toolu_8", "Bash", `grep -n "Schema = " -A25 internal/y.go`),
			post(900, "toolu_6"),
			pre(1260, "toolu_9", "Edit", ""),
			pre(1300, "toolu_10", "Bash", "gofmt -l ."),
			post(1310, "toolu_10"),
		},
		tools: map[string]int64{Lint: 11000, Search: 1000, Test: 160000},
		slow:  []Cmd{{"go test ./...", Test, 160000}, {"gofmt -l .", Lint, 10000}, {"go vet ./...", Lint, 1000}},
	}, {
		// Claude Code, 8 min with no model time: one `true` with no end.
		name:  "claude lone call",
		end:   509,
		calls: []func(*Tracker){pre(0, "toolu_1", "Bash", "true")},
	}, {
		// Claude Code: a subagent's calls arrive on the pane while its
		// Agent call runs, and set that call aside until its end comes.
		name: "claude subagent",
		end:  200,
		calls: []func(*Tracker){
			pre(10, "toolu_1", "Agent", ""),
			pre(11, "toolu_2", "Read", ""),
			post(12, "toolu_2"),
			pre(13, "toolu_3", "Bash", "go test ./..."),
			post(20, "toolu_3"),
			post(100, "toolu_1"),
		},
		tools: map[string]int64{Agent: 86000, Search: 500, Test: 3500},
		slow:  []Cmd{{"go test ./...", Test, 7000}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			e := replay(t, tc.end, tc.calls...)
			if !maps.Equal(e.Tools, tc.tools) || !slices.Equal(e.Slow, tc.slow) {
				t.Errorf("tools %v, want %v\nslow %+v\nwant %+v", e.Tools, tc.tools, e.Slow, tc.slow)
			}
		})
	}
}

func TestLogRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timing.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := "sk-ant-api03-" + strings.Repeat("a", 90)
	l.Add(Event{T: at(10), Kind: Turn, Start: at(0), Ms: 10000, Model: 10000,
		Slow: []Cmd{{Command: "curl -H 'x-api-key: " + key + "' " + strings.Repeat("x ", 300), Kind: Shell, Ms: 5000}}})
	l.Add(Event{T: at(20), Kind: PR, Tab: "t", PR: 7})
	l.Close(time.Second)
	evs, err := Read(path, time.Time{})
	if err != nil || len(evs) != 2 {
		t.Fatalf("read %v %+v", err, evs)
	}
	c := evs[0].Slow[0].Command
	if strings.Contains(c, key[:20]) || len([]rune(c)) > cmdLen || !evs[0].Start.Equal(at(0)) {
		t.Errorf("command %q start %v", c, evs[0].Start)
	}
	if evs[1].PR != 7 || evs[1].V != Version {
		t.Errorf("pr %+v", evs[1])
	}
}

func TestSinkKey(t *testing.T) {
	for _, tc := range []struct{ cmd, want string }{
		{"cd /home/u/repo && go test ./internal/daemon -run TestX", "go test ./internal/daemon -run TestX"},
		{"gh pr checks 175 --watch", "gh pr checks N --watch"},
		{"sleep 30", "sleep N"},
		{"sh scripts/get_test.sh", "sh scripts/get_test.sh"},
		{"cat /tmp/x/out.log | tail -n 20", "cat <path> | tail -n N"},
		{`git commit -m "Fix the thing"`, "git commit -m <str>"},
		{"git show 3f9c2e1ab4d5", "git show <hex>"},
	} {
		if got := SinkKey(tc.cmd); got != tc.want {
			t.Errorf("SinkKey(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

func TestSummarize(t *testing.T) {
	evs := []Event{
		{T: at(0), Kind: Prompt, Tab: "t"},
		{T: at(100), Kind: Turn, Tab: "t", Branch: "fix-x", Start: at(0), Ms: 100000, Model: 50000, Wait: 10000,
			Tools: map[string]int64{Test: 40000}, Slow: []Cmd{{"cd /a/b && go test ./x -run TestA", Test, 30000}}},
		{T: at(300), Kind: Turn, Tab: "t", Start: at(200), Ms: 100000, Model: 100000, Reply: 100000,
			Slow: []Cmd{{"go test ./x -run TestA", Test, 10000}}},
		{T: at(400), Kind: PR, Tab: "t", PR: 3},
		{T: at(500), Kind: Checks, Tab: "t", PR: 3, CI: "fail"},
		{T: at(700), Kind: Checks, Tab: "t", PR: 3, CI: "pass"},
		{T: at(1000), Kind: Merged, Tab: "t", PR: 3},
		{T: at(1100), Kind: Checks, Tab: "t", PR: 3, CI: "pass"},
	}
	s := Summarize(evs, at(0), at(2000), time.UTC)
	if s.Turns != 2 || s.Agent != 190000 || s.Wait != 10000 || s.Reply != 100000 || s.Parts[Test] != 40000 || s.Parts[Model] != 150000 {
		t.Errorf("summary %+v", s)
	}
	if len(s.Days) != 1 || s.Days[0].Total != 200000 {
		t.Errorf("days %+v", s.Days)
	}
	if len(s.Sinks) != 1 || s.Sinks[0] != (Sink{"go test ./x -run TestA", Test, 2, 30000, 40000}) {
		t.Fatalf("sinks %+v", s.Sinks)
	}
	tab := s.Tabs[0]
	if tab.Name != "fix-x" || Between(tab.Prompt, tab.Opened) != "6m 40s" || Between(tab.Opened, tab.CI) != "5m 00s" || Between(tab.CI, tab.Done) != "5m 00s" || tab.CIState != "pass" {
		t.Errorf("tab %+v", tab)
	}
	r := Report(s, 10, "the last 7 days")
	for _, want := range []string{"2 turns", "| Model | 0.0h | 75% |", "`go test ./x -run TestA`", "| fix-x | 2 |"} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
}
