package remote

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestAgentsAndArgv(t *testing.T) {
	look := func(cmd string) error {
		if cmd == "codex" {
			return errors.New("not found")
		}
		return nil
	}
	as := Agents([]config.AgentPreset{{Name: "fast", Agent: "/usr/bin/opencode", Args: []string{"-m", "x"}}}, look)
	var ids []string
	for _, a := range as {
		ids = append(ids, a.ID)
	}
	if want := []string{"claude", "agy", "terminal", "preset:fast"}; !slices.Equal(ids, want) {
		t.Fatalf("agents %q, want %q", ids, want)
	}
	cases := []struct {
		agent, brief, preset string
		cmd                  []string
	}{
		{"claude", "", "", []string{"claude"}},
		{"claude", "fix it; rm -rf /", "", []string{"claude", "fix it; rm -rf /"}}, // one argument, no shell
		{"agy", "go", "", []string{"agy", "-i", "go"}},
		{"terminal", "", "", nil},
		{"preset:fast", "go", "fast", []string{"--prompt", "go"}}, // after the preset's own args
		{"preset:fast", "", "fast", nil},
	}
	for _, c := range cases {
		a := as[slices.IndexFunc(as, func(a Agent) bool { return a.ID == c.agent })]
		preset, cmd := a.Argv(c.brief)
		if preset != c.preset || !slices.Equal(cmd, c.cmd) {
			t.Errorf("%s %q: %q %q, want %q %q", c.agent, c.brief, preset, cmd, c.preset, c.cmd)
		}
	}
}

func TestFolders(t *testing.T) {
	home, repo, plain, closed := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	st := model.State{
		Sessions: []model.Session{{ID: "s1", Name: "main", Order: []string{"w1", "w2", "w3", "w4"}}, {ID: "s2", Name: "other", Order: []string{"w5"}}},
		Workspaces: []model.Workspace{
			{ID: "w1", SessionID: "s1", Path: filepath.Join(repo, ".worktrees", "x"), RepoRoot: filepath.Join(repo, ".worktrees", "x"), WorktreeRoot: repo, Branch: "x"},
			{ID: "w2", SessionID: "s1", Path: plain},
			{ID: "w3", SessionID: "s1", Path: home},
			{ID: "w4", SessionID: "s1", Path: filepath.Join(plain, "gone")},
			{ID: "w5", SessionID: "s2", Path: t.TempDir()},
		},
		Closed: []model.ClosedTab{{Tab: model.Workspace{Path: closed, RepoRoot: closed}, Session: "main"}, {Tab: model.Workspace{Path: plain}, Session: "main"}},
	}
	got := Folders(st, "main", home)
	want := []Folder{{repo, filepath.Base(repo), true}, {plain, filepath.Base(plain), false}, {home, "~", false}, {closed, filepath.Base(closed), true}}
	if !slices.Equal(got, want) {
		t.Fatalf("folders\n%v\nwant\n%v", got, want)
	}
}

func TestNewTabEndpoint(t *testing.T) {
	s, _ := newServer(t)
	dir := t.TempDir()
	var started []NewTab
	s.NewOptions = func(session string) NewOptions {
		return NewOptions{Agents: Agents(nil, func(string) error { return nil }), Folders: []Folder{{Path: dir, Name: "d", Git: true}}}
	}
	s.Start = func(nt NewTab) (string, string, error) {
		if nt.Dir == filepath.Join(dir, "busy") {
			return "", "", errors.New("the guard refused it")
		}
		started = append(started, nt)
		return "tab1", "pane1", nil
	}
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	if st, body := call(t, h, "GET", "/api/new?session=main", token, ""); st != 200 || !strings.Contains(body, `"id":"terminal"`) || !strings.Contains(body, `"git":true`) || strings.Contains(body, "Program") {
		t.Fatalf("options: %d %s", st, body)
	}
	os.Mkdir(filepath.Join(dir, "busy"), 0o755)
	file := filepath.Join(dir, "f")
	os.WriteFile(file, nil, 0o644)
	bad := []struct{ body, want string }{
		{`{"dir":"` + dir + `","agent":"sh -c evil"}`, "pick an agent"},
		{`{"dir":"relative","agent":"terminal"}`, "full path"},
		{`{"dir":"` + filepath.Join(dir, "none") + `","agent":"terminal"}`, "no folder"},
		{`{"dir":"` + file + `","agent":"terminal"}`, "no folder"},
		{`{"dir":"` + dir + `","agent":"claude","worktree":true,"branch":"  "}`, "branch"},
		{`{"dir":"` + dir + `","agent":"terminal","brief":"hi"}`, "no first message"},
		{`{"dir":"` + dir + `","agent":"claude","brief":"--dangerously-skip-permissions"}`, "dash"},
	}
	for _, b := range bad {
		if st, body := call(t, h, "POST", "/api/new", token, b.body); st != 400 || !strings.Contains(body, b.want) {
			t.Errorf("%s: %d %s, want 400 %q", b.body, st, body, b.want)
		}
	}
	if st, body := call(t, h, "POST", "/api/new", token, `{"dir":"`+filepath.Join(dir, "busy")+`","agent":"terminal"}`); st != 409 || !strings.Contains(body, "guard") {
		t.Errorf("refused start: %d %s", st, body)
	}
	if len(started) != 0 {
		t.Fatalf("started %v from bad requests", started)
	}
	st, body := call(t, h, "POST", "/api/new", token, `{"session":"main","dir":" `+dir+`/ ","agent":"terminal","branch":"ignored"}`)
	var out struct{ Tab, Pane string }
	json.Unmarshal([]byte(body), &out)
	if st != 200 || out.Tab != "tab1" || out.Pane != "pane1" {
		t.Fatalf("plain: %d %s", st, body)
	}
	if st, body := call(t, h, "POST", "/api/new", token, `{"session":"main","dir":"`+dir+`","agent":"claude","worktree":true,"branch":" fix-login ","brief":"go"}`); st != 200 {
		t.Fatalf("worktree: %d %s", st, body)
	}
	want := []NewTab{
		{Session: "main", Dir: dir, Agent: "terminal"},
		{Session: "main", Dir: dir, Agent: "claude", Worktree: true, Branch: "fix-login", Brief: "go", Cmd: []string{"claude", "go"}},
	}
	if len(started) != 2 || !equalNewTab(started[0], want[0]) || !equalNewTab(started[1], want[1]) {
		t.Fatalf("started %+v\nwant %+v", started, want)
	}
}

func equalNewTab(a, b NewTab) bool {
	return a.Session == b.Session && a.Dir == b.Dir && a.Agent == b.Agent && a.Worktree == b.Worktree && a.Branch == b.Branch &&
		a.Brief == b.Brief && a.Preset == b.Preset && slices.Equal(a.Cmd, b.Cmd)
}

func TestCloseAndRestartEndpoints(t *testing.T) {
	s, _ := newServer(t)
	var did []string
	s.CloseTab = func(tab string) error { did = append(did, "close "+tab); return nil }
	s.Restart = func(pane string) error {
		if pane == "live" {
			return errors.New("that pane is still running")
		}
		did = append(did, "restart "+pane)
		return nil
	}
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	if st, _ := call(t, h, "POST", "/api/close", token, `{"tab":"w1"}`); st != 200 {
		t.Errorf("close: %d", st)
	}
	if st, _ := call(t, h, "POST", "/api/close", token, `{}`); st != 400 {
		t.Errorf("close without a tab: %d", st)
	}
	if st, _ := call(t, h, "POST", "/api/restart", token, `{"pane":"p1"}`); st != 200 {
		t.Errorf("restart: %d", st)
	}
	if st, body := call(t, h, "POST", "/api/restart", token, `{"pane":"live"}`); st != 409 || !strings.Contains(body, "running") {
		t.Errorf("restart of a running pane: %d %s", st, body)
	}
	if st, _ := call(t, h, "POST", "/api/close", "", `{"tab":"w1"}`); st != 401 {
		t.Errorf("close unpaired: %d", st)
	}
	if want := []string{"close w1", "restart p1"}; !slices.Equal(did, want) {
		t.Errorf("did %q, want %q", did, want)
	}
}
