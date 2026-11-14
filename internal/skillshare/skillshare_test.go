package skillshare

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures under testdata are skillshare v0.30.7's own --json output,
// captured in a temp HOME with two targets (claude, codex). global: a new
// skill (deploy-notes), a disabled one (format-on-save), one with an
// update (lint-rules), an agent, an MCP server in sync and one waiting
// (docs-server), and a hook waiting (format-check). insync: the same
// source synced. project: a project with one new skill. The plugin and
// hook fixtures drop the target catalogs the popup does not read.

// readDir loads one fixture folder as a scope's reads.
func readDir(t *testing.T, dir string) *Read {
	t.Helper()
	r := &Read{}
	for name, file := range map[string]string{"list": "list", "diff": "diff", "mcp": "mcp", "plugins": "plugins", "hooks": "hooks", "status": "status", "check": "check", "checkAgents": "check_agents"} {
		b, err := os.ReadFile(filepath.Join("testdata", dir, file+".json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Parse(name, Result{Stdout: string(b)}); err != nil {
			t.Fatalf("%s/%s: %v", dir, name, err)
		}
	}
	return r
}

func byName(items []Item) map[string]Item {
	out := map[string]Item{}
	for _, it := range items {
		out[string(it.Kind)+":"+it.Name] = it
	}
	return out
}

func TestBuildItems(t *testing.T) {
	items := BuildItems(Global, readDir(t, "global"))
	got := byName(items)
	for _, c := range []struct {
		key     string
		state   State
		targets []string
		src     string
	}{
		{"skills:deploy-notes", New, []string{"claude", "codex"}, "local"},
		{"skills:format-on-save", Off, nil, "local"},
		{"skills:lint-rules", Update, nil, "file:///home/demo/src/skills-repo//skills/lint-rules"},
		{"skills:code-review", InSync, nil, "local"},
		{"agents:reviewer", InSync, nil, "local"},
		{"mcp:demo-server", InSync, []string{"claude", "codex"}, "MCP server"},
		{"mcp:docs-server", Changed, []string{"claude"}, "MCP server"},
		{"hooks:format-check", Changed, []string{"claude"}, "claude"},
	} {
		it, ok := got[c.key]
		if !ok {
			t.Errorf("%s missing", c.key)
			continue
		}
		if it.State != c.state || !reflect.DeepEqual(it.Targets, c.targets) || it.Src != c.src {
			t.Errorf("%s = %s %v %q, want %s %v %q", c.key, it.State, it.Targets, it.Src, c.state, c.targets, c.src)
		}
	}
	if n := len(items); n != 9 {
		t.Errorf("%d items, want 9 (5 skills, 1 agent, 2 MCP, 1 hook)", n)
	}
	if u := got["skills:lint-rules"]; u.Update != "lint-rules" || u.Version != "bfe1025" {
		t.Errorf("lint-rules update %q version %q", u.Update, u.Version)
	}
	if d := got["mcp:docs-server"]; d.Reason != "claude: add" {
		t.Errorf("docs-server reason %q", d.Reason)
	}
}

func TestInSyncOverview(t *testing.T) {
	reads := map[string]*Read{Global: readDir(t, "insync")}
	items := BuildItems(Global, reads[Global])
	known, ts := Targets(reads, "all", items)
	if !known || len(ts) != 2 || ts[0].N != 0 || ts[1].N != 0 {
		t.Fatalf("targets %v %v", known, ts)
	}
	pending := 0
	for _, it := range items {
		if it.WaitsForSync() {
			pending++
		}
	}
	a := Overview(false, nil, false, pending, nil, len(items), len(ts))
	if a.Kind != "ok" || a.Headline != "Everything is in sync" {
		t.Fatalf("answer %+v", a)
	}
}

func TestOverviewWaitingWithoutTargets(t *testing.T) {
	a := Overview(false, nil, false, 1, nil, 1, 0)
	if a.Kind != "waiting" || a.Headline != "1 resource to sync" || a.Sub != "1 resource waiting" {
		t.Fatalf("answer %+v", a)
	}
}

func TestDiffTargetFailure(t *testing.T) {
	r := &Read{}
	err := r.Parse("diff", Result{Stdout: `{"targets":[{"name":"claude","error":"permission denied","items":null}]}`})
	if err == nil || !strings.Contains(err.Error(), "claude: permission denied") {
		t.Fatalf("target error lost: %v", err)
	}
}

func TestDriftOverview(t *testing.T) {
	reads := map[string]*Read{Global: readDir(t, "global"), Project: readDir(t, "project")}
	var items []Item
	for _, s := range []string{Global, Project} {
		items = append(items, BuildItems(s, reads[s])...)
	}
	_, ts := Targets(reads, "all", items)
	// claude: deploy-notes, lint-rules (installed, not synced yet) and
	// proj-style from diff, docs-server and format-check from their
	// plans; codex: deploy-notes and lint-rules.
	want := []Target{{"claude", 5}, {"codex", 2}}
	if !reflect.DeepEqual(ts, want) {
		t.Fatalf("targets %v, want %v", ts, want)
	}
	var lag []Target
	pending := 0
	for _, it := range items {
		if it.WaitsForSync() {
			pending++
		}
	}
	for _, x := range ts {
		if x.N > 0 {
			lag = append(lag, x)
		}
	}
	a := Overview(false, nil, false, pending, lag, len(items), len(ts))
	if a.Headline != "2 targets need a sync" || a.Sub != "4 resources waiting for claude and codex" {
		t.Fatalf("answer %+v", a)
	}
	if got := SyncScopes(items, reads, "all"); !reflect.DeepEqual(got, []string{Global, Project}) {
		t.Fatalf("sync scopes %v", got)
	}
	if got := SyncScopes(items, reads, Global); !reflect.DeepEqual(got, []string{Global}) {
		t.Fatalf("global sync scopes %v", got)
	}
	g := Group(items, "all", Skills)
	names := func(l []Item) (out []string) {
		for _, it := range l {
			out = append(out, it.Name)
		}
		return out
	}
	if !reflect.DeepEqual(names(g.Sync), []string{"deploy-notes", "proj-style"}) || !reflect.DeepEqual(names(g.Update), []string{"lint-rules"}) ||
		!reflect.DeepEqual(names(g.Off), []string{"format-on-save"}) || !reflect.DeepEqual(names(g.InSync), []string{"code-review", "release-notes"}) {
		t.Fatalf("groups %+v", g)
	}
	if c := Counts(items, Project); c[Skills] != 1 || c[Agents] != 0 {
		t.Fatalf("project counts %v", c)
	}
}

// A pending or failed read makes its rows unknown and its kinds' counts
// unsure, never zero or in sync.
func TestPendingReads(t *testing.T) {
	r := readDir(t, "insync")
	r.Pending = map[string]bool{"diff": true}
	r.Errors = map[string]string{"mcp": "boom"}
	items := BuildItems(Global, r)
	for _, it := range items {
		switch it.Kind {
		case Skills, Agents, MCP:
			if it.State != Unknown && it.State != Off {
				t.Errorf("%s is %s with diff pending, mcp failed", it.Name, it.State)
			}
		case Hooks:
			if it.State == Unknown {
				t.Errorf("hook %s unknown though hooks answered", it.Name)
			}
		}
	}
	u := Unsure(map[string]*Read{Global: r}, "all")
	if !u[MCP] || u[Skills] || u[Hooks] {
		t.Fatalf("unsure %v", u)
	}
	_, ts := Targets(map[string]*Read{Global: r}, "all", items)
	for _, x := range ts {
		if x.N != 0 {
			t.Fatalf("a pending diff counted %v", ts)
		}
	}
}

func TestParseFailure(t *testing.T) {
	b, err := os.ReadFile("testdata/list_error.txt")
	if err != nil {
		t.Fatal(err)
	}
	err = (&Read{}).Parse("list", Result{Stdout: string(b), Code: 1})
	if err == nil || !strings.HasPrefix(err.Error(), "Restore it from version control") {
		t.Fatalf("error %v", err)
	}
	err = (&Read{}).Parse("diff", Result{Stdout: `{"error":".skillshare/ has no config.yaml"}`, Code: 1})
	if err == nil || err.Error() != ".skillshare/ has no config.yaml" {
		t.Fatalf("json error %v", err)
	}
}

func TestPlugins(t *testing.T) {
	// No plugin was installable offline, so this one is written by hand in
	// the shape `plugin list --json` has.
	r := &Read{}
	err := r.Parse("plugins", Result{Stdout: `{"packages":{"lint":{"source":"https://github.com/acme/lint.git","version":"1.2.0",
		"bindings":{"claude":{"id":"lint@acme"},"codex":{"pending":true}}},"idle":{"bindings":{}}}}`})
	if err != nil {
		t.Fatal(err)
	}
	got := byName(BuildItems(Global, r))
	if l := got["plugins:lint"]; l.State != Changed || l.Reason != "pending in codex" || l.Src != "github.com/acme/lint" || l.Version != "1.2.0" {
		t.Fatalf("lint %+v", l)
	}
	if i := got["plugins:idle"]; i.State != New {
		t.Fatalf("idle %+v", i)
	}
}

func TestHookNotices(t *testing.T) {
	r := &Read{}
	err := r.Parse("hooks", Result{Stdout: `{"source":{"entries":{"fmt":{"enabled":false}}},
		"plan":{"changes":[{"target":"claude","action":"conflict","message":"claude has its own hook"}],"warnings":["codex: hooks are off"]}}`})
	if err != nil {
		t.Fatal(err)
	}
	items := BuildItems(Global, r)
	var notices []string
	for _, it := range items {
		if it.Notice {
			notices = append(notices, it.Reason)
		} else if it.Name != "fmt" || it.State != Off {
			t.Errorf("hook %+v", it)
		}
	}
	if !reflect.DeepEqual(notices, []string{"claude: conflict · claude has its own hook", "codex: hooks are off"}) {
		t.Fatalf("notices %q", notices)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	meta, body := SplitFrontmatter("---\nname: a\ndescription: >\n  two\n  lines\ntags:\n  - x\n---\n# A\n")
	want := []KV{{"name", "a"}, {"description", "two lines"}, {"tags", "x"}}
	if !reflect.DeepEqual(meta, want) || body != "# A\n" {
		t.Fatalf("%q %q", meta, body)
	}
	if meta, body := SplitFrontmatter("# none\n"); meta != nil || body != "# none\n" {
		t.Fatalf("no frontmatter: %q %q", meta, body)
	}
}

// Every value under env, headers and bearerToken is masked; the names
// stay. The fixture's server reads its token with fromEnv; the second
// file has literal headers and a bearer token.
func TestMCPServerMasks(t *testing.T) {
	b, err := os.ReadFile("testdata/home/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := MCPServer(string(b), "demo-server")
	want := []KV{{"command", "npx"}, {"args", "demo-mcp --port 3000"}, {"env", "API_TOKEN = " + Mask + ", REGION = " + Mask}, {"targets", "claude codex"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("demo-server\n got %q\nwant %q", got, want)
	}
	yaml := "mcp:\n  servers:\n    api:\n      url: https://example.com/mcp\n      headers:\n        Authorization: Bearer sk-live-123\n        X-Team: \"core\"\n      bearerToken: { fromEnv: TOKEN }\n"
	got = MCPServer(yaml, "api")
	want = []KV{{"url", "https://example.com/mcp"}, {"headers", "Authorization = " + Mask + ", X-Team = " + Mask}, {"bearerToken", Mask}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("api\n got %q\nwant %q", got, want)
	}
	for _, kv := range got {
		if strings.Contains(kv.Value, "sk-live") || strings.Contains(kv.Value, "core") || strings.Contains(kv.Value, "TOKEN") {
			t.Fatalf("a secret leaked: %q", kv)
		}
	}
	if MCPServer(yaml, "nope") != nil {
		t.Fatal("a server the file lacks")
	}
}

func TestMCPServerMasksYAMLValues(t *testing.T) {
	for _, fields := range []string{
		"      bearerToken: |\n        private-token\n",
		"      env:\n        REGION: \"private\n        metadata: continuation\"\n",
		"      headers: {Authorization: 'private-token',\n        X-Private: 'private-header'}\n",
		"      headers: &headers\n        Authorization: private-token\n      env: *headers\n",
		"      adapters:\n        pi: {bearerToken: private-token, env: {REGION: private-region}}\n",
	} {
		t.Run(fields, func(t *testing.T) {
			got := MCPServer("mcp:\n  servers:\n    api:\n"+fields, "api")
			if len(got) == 0 {
				t.Fatal("server missing")
			}
			for _, row := range got {
				if strings.Contains(row.Value, "private") || strings.Contains(row.Value, "metadata") || strings.Contains(row.Value, "continuation") {
					t.Fatalf("secret leaked: %q", got)
				}
			}
		})
	}
}

func TestUpdateCommandError(t *testing.T) {
	c := Client{Run: func(context.Context, string, ...string) Result {
		return Result{Code: 1, Stdout: `{"error":"repository unavailable"}`}
	}}
	got := c.UpdateItems(context.Background(), []Item{{Kind: Skills, Update: "lint"}}, false, "", func(string) {})
	if !got.Err || got.Text != "repository unavailable" {
		t.Fatalf("failed update reported success: %+v", got)
	}
}

func TestCloneURL(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Clone
		ok   bool
	}{
		{"acme/skills", Clone{URL: "https://github.com/acme/skills.git"}, true},
		{"acme/skills/skills/a", Clone{URL: "https://github.com/acme/skills.git"}, true},
		{"https://github.com/acme/skills/tree/dev/skills/a", Clone{URL: "https://github.com/acme/skills.git", Branch: "dev"}, true},
		{"gitlab.com/acme/skills.git", Clone{URL: "https://gitlab.com/acme/skills.git"}, true},
		{"git@github.com:acme/skills.git", Clone{URL: "git@github.com:acme/skills.git"}, true},
		{"file:///srv/skills", Clone{URL: "file:///srv/skills"}, true},
		{"--upload-pack=x", Clone{}, false},
		{"skills", Clone{}, false},
	} {
		got, ok := CloneURL(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("CloneURL(%q) = %+v %v, want %+v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDiscoverAuditInstall(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	p := ParseDiscover(Result{Stdout: read("install_dryrun.json")})
	if p.Err != "" || len(p.Choices) != 2 || p.Choices[0] != (Choice{"bad-skill", "skills/bad-skill"}) {
		t.Fatalf("plan %+v", p)
	}
	if p := ParseDiscover(Result{Stdout: `{"skills":[]}`}); !strings.HasPrefix(p.Err, "Upgrade skillshare") {
		t.Fatalf("no plan: %+v", p)
	}
	if p := ParseDiscover(Result{Stdout: `{"planned":[{"name":"A","path":"a"},{"name":"a","path":"b"}]}`}); !strings.HasPrefix(p.Err, "Ambiguous") {
		t.Fatalf("ambiguous: %+v", p)
	}
	a := ParseAudit([]AuditPart{{"skills/bad-skill", read("audit_blocked.json")}, {"skills/lint-rules", `{"results":[{"isBlocked":false}]}`}}, "/home/demo/src/skills-repo")
	if a.Unknown || !reflect.DeepEqual(a.Blocked, []string{"bad-skill"}) || len(a.Findings) != 3 || a.Findings[0].Severity != "CRITICAL" || a.Findings[0].File != "SKILL.md" || a.Findings[0].Line != 10 {
		t.Fatalf("audit %+v", a)
	}
	if a := ParseAudit([]AuditPart{{"x", "not json"}}, ""); !a.Unknown {
		t.Fatal("an unreadable audit is unknown")
	}
	if done, e := InstallResult(Result{Stdout: read("install_ok.json")}); e != "" || !reflect.DeepEqual(done, []string{"lint-rules"}) {
		t.Fatalf("install %v %q", done, e)
	}
	if _, e := InstallResult(Result{Stdout: `{"skills":["a"]}`, Code: 1, Stderr: "boom"}); e != "Installed: a. boom skillshare install exited with 1." {
		t.Fatalf("exit code wins: %q", e)
	}
	if _, e := InstallResult(Result{Stdout: `{"skills":[],"skipped":["a"]}`}); !strings.Contains(e, "already installed") {
		t.Fatalf("skipped: %q", e)
	}
}

func TestArgs(t *testing.T) {
	skill := Item{Kind: Skills, Scope: Project, Path: "a/b", Rel: "a/b", Update: "a/b"}
	agent := Item{Kind: Agents, Scope: Global, Path: "rev.md", Rel: "rev", Disabled: true}
	for _, c := range []struct {
		got, want []string
	}{
		{ToggleArgs(skill, "disable"), []string{"disable", "a/b", "-p"}},
		{ToggleArgs(skill, "enable"), nil},
		{ToggleArgs(agent, "enable"), []string{"enable", "rev.md", "--kind", "agent", "-g"}},
		{UpdateArgs(skill, false), []string{"update", "a/b", "--json", "-p"}},
		{UpdateArgs(agent, true), []string{"update", "agents", "--all", "--json", "-g"}},
		{UninstallArgs(skill), []string{"uninstall", "a/b", "--json", "-p", "--force"}},
		{UninstallArgs(agent), []string{"uninstall", "agents", "rev", "--json", "-g"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// Refresh runs each read on its own: a slow one leaves the others
// answered and itself pending, and old answers stay until new ones land.
func TestStoreRefresh(t *testing.T) {
	slow := make(chan struct{})
	var mu sync.Mutex
	var ran [][]string
	c := Demo()
	demo := c.Run
	c.Run = func(ctx context.Context, dir string, args ...string) Result {
		mu.Lock()
		ran = append(ran, args)
		mu.Unlock()
		if args[0] == "plugin" {
			<-slow
		}
		return demo(ctx, dir, args...)
	}
	var s Store
	landed := make(chan struct{}, 64)
	s.Refresh(c.Serve, "", true, func() { landed <- struct{}{} })
	for range 7 { // 5 reads, 2 checks
		<-landed
	}
	v := s.View()
	r := v.Reads[Global]
	if !r.Pending["plugins"] || r.Pending["list"] || r.List == nil || v.Version != "0.30.7" {
		t.Fatalf("pending %v list %d version %q", r.Pending, len(r.List), v.Version)
	}
	if !s.Running() {
		t.Fatal("a refresh with a read out is running")
	}
	close(slow)
	<-landed // plugins
	<-landed // done
	if s.Running() {
		t.Fatal("still running")
	}
	for _, a := range ran {
		if !slices.Contains(a, "--json") || !slices.Contains(a, "-g") {
			t.Fatalf("ran %q", a)
		}
	}
	// A second refresh keeps the list until its own answer lands.
	block := make(chan struct{})
	c.Run = func(ctx context.Context, dir string, args ...string) Result {
		<-block
		return demo(ctx, dir, args...)
	}
	s.Refresh(c.Serve, "", false, func() {})
	if v := s.View(); v.Reads[Global].List == nil || !v.Reads[Global].Pending["list"] {
		t.Fatal("old answers should stay while a refresh runs")
	}
	close(block)
}

func TestFindProject(t *testing.T) {
	dir := t.TempDir()
	if FindProject(dir) != "" {
		t.Fatal("an empty folder is no project")
	}
	os.MkdirAll(filepath.Join(dir, "skillshare"), 0o755)
	os.WriteFile(filepath.Join(dir, "skillshare", "config.yaml"), nil, 0o644)
	if FindProject(dir) != dir {
		t.Fatal("skillshare/config.yaml makes a project")
	}
}

// The demo's previews read the captured source: SKILL.md split from its
// frontmatter, the agent's file, and the MCP server masked.
func TestDemoPreview(t *testing.T) {
	c := Demo()
	r := &Read{}
	for _, n := range []string{"status", "mcp"} {
		file := map[string]string{"status": "status", "mcp": "mcp"}[n]
		b, _ := os.ReadFile("testdata/global/" + file + ".json")
		if err := r.Parse(n, Result{Stdout: string(b)}); err != nil {
			t.Fatal(err)
		}
	}
	p := c.LoadPreview(Item{Kind: Skills, Rel: "code-review", Label: "code-review"}, r)
	if p.Err != "" || !slices.Equal(p.Files, []string{"SKILL.md", "checklist.md"}) || p.Meta[0] != (KV{"name", "code-review"}) || !strings.HasPrefix(p.Body, "\n# code-review") {
		t.Fatalf("skill preview %+v", p)
	}
	if p := c.LoadPreview(Item{Kind: Agents, Rel: "reviewer", Label: "reviewer"}, r); p.Err != "" || len(p.Meta) != 3 {
		t.Fatalf("agent preview %+v", p)
	}
	if p := c.LoadPreview(Item{Kind: MCP, Name: "demo-server"}, r); p.Err != "" || p.MCP[2].Value != "API_TOKEN = "+Mask+", REGION = "+Mask {
		t.Fatalf("mcp preview %+v", p)
	}
}

// TestLiveCLI runs every read against a real skillshare on PATH, in the
// HOME the caller set up; PITWALL_SKILLSHARE_LIVE=1 turns it on.
func TestLiveCLI(t *testing.T) {
	if os.Getenv("PITWALL_SKILLSHARE_LIVE") != "1" {
		t.Skip("set PITWALL_SKILLSHARE_LIVE=1 with skillshare on PATH")
	}
	c := Local()
	if !c.Installed() {
		t.Fatal("skillshare is not on PATH")
	}
	var s Store
	done := make(chan struct{}, 64)
	s.Refresh(c.Serve, "", true, func() { done <- struct{}{} })
	for s.Running() {
		<-done
	}
	v := s.View()
	if errs := v.Reads[Global].Errors; len(errs) > 0 {
		t.Fatalf("read errors %v", errs)
	}
	if len(v.Items) == 0 || v.Version == "" {
		t.Fatalf("%d items, version %q", len(v.Items), v.Version)
	}
	for _, it := range v.Items {
		t.Logf("%s %s %s %v", it.Kind, it.Name, it.State, it.Targets)
	}
	// A source repository, such as a file:// one, goes through the clone
	// and the dry run.
	if src := os.Getenv("PITWALL_SKILLSHARE_SOURCE"); src != "" {
		p := c.Discover(context.Background(), src, Global, "")
		defer Drop(p.Dir)
		if p.Err != "" || len(p.Choices) == 0 {
			t.Fatalf("discover %+v", p)
		}
		t.Logf("plan %+v", p.Choices)
	}
}

// Project changes must take effect while a slow read from the old project runs.
func TestStoreRefreshChangesProject(t *testing.T) {
	oldRead, release := make(chan struct{}), make(chan struct{})
	releaseNew := make(chan struct{})
	c := Demo()
	demo := c.Run
	c.Run = func(ctx context.Context, dir string, args ...string) Result {
		if dir == "/old" && args[0] == "list" {
			close(oldRead)
			<-release
		}
		if dir == "/new" && args[0] == "list" {
			<-releaseNew
		}
		return demo(ctx, dir, args...)
	}
	var s Store
	changed := make(chan struct{}, 128)
	notify := func() { changed <- struct{}{} }
	s.Refresh(c.Serve, "/old", false, notify)
	<-oldRead
	s.Refresh(c.Serve, "/new", false, notify)
	v := s.View()
	if v.Project != "/new" || !v.Reads[Project].Pending["list"] {
		t.Fatalf("old project still active: %+v", v)
	}
	close(release)
	close(releaseNew)
	for deadline := time.After(5 * time.Second); ; {
		if !s.Running() {
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("new project's reads never finished")
		}
	}
	if v := s.View(); v.Project != "/new" || len(v.Reads[Project].List) == 0 {
		t.Fatalf("new project not loaded: %+v", v)
	}
}

// A mutation's refresh must not be lost behind an older read in flight.
func TestStoreRefreshQueuesAnotherRead(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var lists, checks int
	var mu sync.Mutex
	c := Demo()
	demo := c.Run
	c.Run = func(ctx context.Context, dir string, args ...string) Result {
		if args[0] == "check" && len(args) > 1 && args[1] != "agents" {
			mu.Lock()
			checks++
			mu.Unlock()
		}
		if args[0] == "list" {
			mu.Lock()
			lists++
			n := lists
			mu.Unlock()
			if n == 1 {
				close(started)
				<-release
			}
		}
		return demo(ctx, dir, args...)
	}
	var s Store
	changed := make(chan struct{}, 128)
	notify := func() { changed <- struct{}{} }
	s.Refresh(c.Serve, "", false, notify)
	<-started
	s.Refresh(c.Serve, "", true, notify)
	s.Refresh(c.Serve, "", false, notify)
	close(release)
	for deadline := time.After(5 * time.Second); ; {
		mu.Lock()
		n := lists
		mu.Unlock()
		if !s.Running() && n == 2 {
			mu.Lock()
			got := checks
			mu.Unlock()
			if got != 2 {
				t.Fatalf("forced update check was lost: %d checks", got)
			}
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("queued refresh never finished")
		}
	}
}

func TestHookToggle(t *testing.T) {
	hook := Item{Kind: Hooks, Scope: Project, Name: "fmt", Declared: true, Disabled: true}
	if got := ToggleArgs(hook, "enable"); !slices.Equal(got, []string{"hooks", "enable", "fmt", "-p"}) {
		t.Fatalf("hook args %v", got)
	}
	c := Client{Run: func(ctx context.Context, dir string, args ...string) Result {
		if dir != "/project" || !slices.Equal(args, []string{"hooks", "enable", "fmt", "-p"}) {
			t.Fatalf("hook command %q %v", dir, args)
		}
		return Result{}
	}}
	if out := c.Toggle(context.Background(), hook, "enable", "/project", func(string) {}); out.Err {
		t.Fatalf("hook failed: %+v", out)
	}
	if got := UninstallArgs(hook); !slices.Equal(got, []string{"hooks", "remove", "fmt", "-p"}) {
		t.Fatalf("hook remove args %v", got)
	}
	hook.Root = "/other"
	if got := ToggleArgs(hook, "enable"); got != nil {
		t.Fatalf("project-root hook cannot use the scoped CLI action: %v", got)
	}
}

// ParseGit reads the branch line's tracking counts and counts changed
// files; a source with work of its own and a remote syncs both ways.
func TestParseGit(t *testing.T) {
	g := ParseGit("/src", "## main...origin/main [ahead 1, behind 2]\n M a.md\n?? b.md\n")
	if g.Branch != "main" || g.Upstream != "origin/main" || g.Ahead != 1 || g.Behind != 2 || g.Dirty != 2 || !g.BothWays() {
		t.Fatalf("%+v", g)
	}
	if l := g.Line(); l != "↓2 behind · 2 uncommitted · ↑1 to push" {
		t.Fatalf("line %q", l)
	}
	if g := ParseGit("/src", "## No commits yet on main\n"); g.Upstream != "" || g.Line() != "no remote" || g.BothWays() {
		t.Fatalf("no remote: %+v %q", g, g.Line())
	}
	if (Git{Read: true}).Line() != "no git" || (Git{}).Line() != "could not be read" {
		t.Fatal("not a repo, or unread")
	}
}
