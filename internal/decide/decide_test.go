package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeKey = "ts_test_key_0123456789abcdefghijklmnop"

// fakeJev is a stand-in for api.typesafe.ai: it records each request and
// answers with reply, or calls handle when set.
type fakeJev struct {
	mu     sync.Mutex
	bodies []string
	auth   []string
	reply  string
	handle func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeJev) start(t *testing.T) (*httptest.Server, *Jev) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if f.handle != nil {
			f.handle(w, r)
			return
		}
		io.WriteString(w, f.reply)
	}))
	t.Cleanup(srv.Close)
	j := NewJev(fakeKey, "")
	j.url = srv.URL
	return srv, j
}

func TestJevRequestAndAnswers(t *testing.T) {
	f := &fakeJev{reply: `{"model":"jev-1.13.0","answers":{
		"pick":{"type":"choice","choice":"billing","probabilities":{"billing":0.88,"technical":0.12},"confidence":0.81},
		"rate":{"type":"score","score":1.05,"legend":{"0":"Calm","1":"Frustrated","2":"Very angry"},"probabilities":{"0":0.0,"1":0.95,"2":0.05},"confidence":0.92},
		"yes":{"type":"noul","noul":0.95}},
		"usage":{"input_tokens":304,"output_tokens":18}}`}
	_, j := f.start(t)
	c := &Client{P: j, Counts: &Counters{}}
	qs := map[string]Question{
		"pick": {Type: Choice, Instructions: "Which team?", Criteria: map[string]string{"billing": "money", "technical": "bugs"}},
		"rate": {Type: Score, Instructions: "How frustrated?", Criteria: []string{"Calm", "Frustrated", "Very angry"}},
		"yes":  {Type: Noul, Instructions: "Urgent?"},
	}
	ans, err := c.Ask(context.Background(), FeatureTriage, "p1", "Help! My payouts failed.", qs)
	if err != nil {
		t.Fatal(err)
	}
	if a := ans["pick"]; a.Choice != "billing" || a.Probabilities["technical"] != 0.12 || a.Confidence != 0.81 {
		t.Errorf("choice = %+v", a)
	}
	if a := ans["rate"]; a.Score != 1.05 || a.Probabilities["1"] != 0.95 || a.Confidence != 0.92 {
		t.Errorf("score = %+v", a)
	}
	if a := ans["yes"]; a.Noul != 0.95 {
		t.Errorf("noul = %+v", a)
	}
	if f.auth[0] != "Bearer "+fakeKey {
		t.Errorf("auth header = %q", f.auth[0])
	}
	var body struct {
		Model     string                     `json:"model"`
		State     string                     `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "jev-latest" || body.State != "Help! My payouts failed." || len(body.Questions) != 3 {
		t.Errorf("body = %s", f.bodies[0])
	}
	for id, want := range map[string]string{
		"pick": `{"type":"choice","instructions":"Which team?","criteria":{"billing":"money","technical":"bugs"}}`,
		"rate": `{"type":"score","instructions":"How frustrated?","criteria":["Calm","Frustrated","Very angry"]}`,
		"yes":  `{"type":"noul","instructions":"Urgent?"}`,
	} {
		if got := string(body.Questions[id]); got != want {
			t.Errorf("question %s = %s, want %s", id, got, want)
		}
	}
	if n := c.Counts.Today(time.Now())[FeatureTriage]; n.Calls != 1 || n.Errors != 0 {
		t.Errorf("counts = %+v", n)
	}
}

func TestBadAnswersAreErrors(t *testing.T) {
	for name, reply := range map[string]string{
		"missing":    `{"answers":{}}`,
		"wrong type": `{"answers":{"q":{"type":"noul","noul":0.5}}}`,
		"not option": `{"answers":{"q":{"type":"choice","choice":"maybe","probabilities":{"maybe":1}}}}`,
		"bad prob":   `{"answers":{"q":{"type":"choice","choice":"a","probabilities":{"a":1.5}}}}`,
		"not json":   `<html>`,
		"no answers": `{"model":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeJev{reply: reply}
			_, j := f.start(t)
			c := &Client{P: j, Counts: &Counters{}}
			ans, err := c.Ask(context.Background(), FeatureApprovals, "", "s", map[string]Question{
				"q": {Type: Choice, Instructions: "?", Criteria: map[string]string{"a": "", "b": ""}},
			})
			if err == nil || ans != nil {
				t.Fatalf("ans, err = %v, %v; want an error", ans, err)
			}
			if n := c.Counts.Today(time.Now())[FeatureApprovals]; n.Errors != 1 {
				t.Errorf("errors = %d", n.Errors)
			}
		})
	}
}

func TestTimeoutFallsThrough(t *testing.T) {
	release := make(chan struct{})
	f := &fakeJev{handle: func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}}
	_, j := f.start(t)
	defer close(release)
	c := &Client{P: j, Timeout: 50 * time.Millisecond, Counts: &Counters{}}
	start := time.Now()
	_, err := c.Ask(context.Background(), FeatureTriage, "p", "s", TriageQuestions())
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
	if n := c.Counts.Today(time.Now())[FeatureTriage]; n.Calls != 1 || n.Errors != 1 {
		t.Errorf("counts = %+v", n)
	}
}

// A server that echoes the Authorization header in its error must not get
// the key into the error text.
func TestKeyNeverInErrors(t *testing.T) {
	f := &fakeJev{handle: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad key %s"}`, r.Header.Get("Authorization"))
	}}
	_, j := f.start(t)
	c := &Client{P: j, Secrets: []string{fakeKey}}
	_, err := c.Ask(context.Background(), FeatureTest, "", "s", map[string]Question{"q": {Type: Noul, Instructions: "?"}})
	if err == nil || strings.Contains(err.Error(), fakeKey) || strings.Contains(err.Error(), "ts_test_key") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want the status", err)
	}
	// Even without Secrets, the provider scrubs its own key.
	_, err = j.Ask(context.Background(), Request{State: "s", Questions: map[string]Question{"q": {Type: Noul, Instructions: "?"}}})
	if err == nil || strings.Contains(err.Error(), fakeKey) {
		t.Fatalf("provider err = %v", err)
	}
	if s := fmt.Sprintf("%v %+v %#v", j, *j, j.Key); strings.Contains(s, fakeKey) {
		t.Errorf("formatting a Jev shows the key: %s", s)
	}
}

func TestRedactionBeforeSending(t *testing.T) {
	f := &fakeJev{reply: `{"answers":{"q":{"type":"noul","noul":0.1}}}`}
	_, j := f.start(t)
	c := &Client{P: j, Secrets: []string{"hunter2-verbatim"}}
	secrets := []string{
		"sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWx",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
		"AKIAABCDEFGHIJKLMNOP",
		"supersecretvalue1",
		"pa55word-in-url",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
		"tok3n-in-header-value",
		"hunter2-verbatim",
		"xoxb-123456789012-abcdefghij",
		"flagpassword99",
	}
	state := map[string]any{
		"input": map[string]any{
			"command": "export ANTHROPIC_API_KEY=" + secrets[0] + " && git push https://user:" + secrets[4] + "@example.com/r.git",
			"env":     []any{"GITHUB_TOKEN=" + secrets[1], "AWS_ACCESS_KEY_ID=" + secrets[2]},
		},
		"file":   "DB_PASSWORD=\"" + secrets[3] + "\"\nPORT=8080\n",
		"jwt":    "token " + secrets[5],
		"pem":    "-----BEGIN RSA PRIVATE KEY-----\n" + secrets[6] + "\n-----END RSA PRIVATE KEY-----",
		"header": "curl -H 'Authorization: Bearer " + secrets[7] + "' https://api.example.com",
		"prompt": "my password is " + secrets[8],
		"slack":  secrets[9],
		"flag":   "mysql --password=" + secrets[10],
	}
	if _, err := c.Ask(context.Background(), FeatureApprovals, "", state, map[string]Question{"q": {Type: Noul, Instructions: "?"}}); err != nil {
		t.Fatal(err)
	}
	body := f.bodies[0]
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Errorf("secret %q reached the request body: %s", s, body)
		}
	}
	for _, keep := range []string{"PORT=8080", "git push", "example.com/r.git", "curl -H"} {
		if !strings.Contains(body, keep) {
			t.Errorf("redaction removed %q too: %s", keep, body)
		}
	}
}

func TestRateLimitPerPane(t *testing.T) {
	f := &fakeJev{reply: `{"answers":{"q":{"type":"noul","noul":0.1}}}`}
	_, j := f.start(t)
	now := time.Unix(1000, 0)
	c := &Client{P: j, Now: func() time.Time { return now }}
	q := map[string]Question{"q": {Type: Noul, Instructions: "?"}}
	for i := range perPane {
		if _, err := c.Ask(context.Background(), FeatureTriage, "a", "s", q); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := c.Ask(context.Background(), FeatureTriage, "a", "s", q); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want rate limited", err)
	}
	if _, err := c.Ask(context.Background(), FeatureTriage, "b", "s", q); err != nil {
		t.Fatalf("another pane: %v", err)
	}
	now = now.Add(window)
	if _, err := c.Ask(context.Background(), FeatureTriage, "a", "s", q); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	if len(f.bodies) != perPane+2 {
		t.Errorf("requests = %d", len(f.bodies))
	}
}

func TestCommandProvider(t *testing.T) {
	if os.Getenv("DECIDE_HELPER") != "" {
		return
	}
	c := &Client{P: Command{Argv: []string{os.Args[0], "-test.run=TestHelperCommand"}}, Timeout: 5 * time.Second}
	t.Setenv("DECIDE_HELPER", "answer")
	ans, err := c.Ask(context.Background(), FeatureAgents, "", "screen text API_KEY=abcd1234secret", ScreenQuestions())
	if err != nil {
		t.Fatal(err)
	}
	if s, conf := Screen(ans); s != ScreenWaiting || conf != 0.9 {
		t.Errorf("screen = %q %v", s, conf)
	}
	t.Setenv("DECIDE_HELPER", "sleep")
	c.Timeout = 100 * time.Millisecond
	if _, err := c.Ask(context.Background(), FeatureAgents, "", "s", ScreenQuestions()); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("slow command: err = %v", err)
	}
	t.Setenv("DECIDE_HELPER", "fail")
	c.Timeout = 5 * time.Second
	if _, err := c.Ask(context.Background(), FeatureAgents, "", "s", ScreenQuestions()); err == nil || !strings.Contains(err.Error(), "model offline") {
		t.Errorf("failing command: err = %v", err)
	}
}

// TestHelperCommand is the command TestCommandProvider runs: it reads the
// request and answers like a local classifier.
func TestHelperCommand(t *testing.T) {
	switch os.Getenv("DECIDE_HELPER") {
	case "answer":
		var r Request
		if err := json.NewDecoder(os.Stdin).Decode(&r); err != nil {
			os.Exit(3)
		}
		if s, _ := r.State.(string); strings.Contains(s, "abcd1234secret") || r.Questions["status"].Type != Choice {
			os.Exit(4) // the secret must have been redacted
		}
		fmt.Print(`{"answers":{"status":{"type":"choice","choice":"waiting for input","probabilities":{"waiting for input":0.95,"working":0.05,"asking approval":0,"done":0,"idle":0},"confidence":0.9}}}`)
		os.Exit(0)
	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stderr, "model offline")
		os.Exit(1)
	}
}

func TestCredentials(t *testing.T) {
	t.Setenv(KeyEnv, "")
	path := CredentialsPath(t.TempDir())
	if k, src, err := LoadKey(path); k != "" || src != "" || err != nil {
		t.Fatalf("no file: %q %q %v", k, src, err)
	}
	if err := SaveKey(path, "bad key"); err == nil {
		t.Error("a key with a space saved")
	}
	if err := SaveKey(path, fakeKey); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if k, src, err := LoadKey(path); k != fakeKey || src != FromFile || err != nil {
		t.Fatalf("load: %q %q %v", k, src, err)
	}
	t.Setenv(KeyEnv, "env_key_123456789")
	if k, src, _ := LoadKey(path); k != "env_key_123456789" || src != FromEnv {
		t.Errorf("env should win: %q %q", k, src)
	}
	t.Setenv(KeyEnv, "")
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0o644)
		_, _, err := LoadKey(path)
		if err == nil || strings.Contains(err.Error(), fakeKey) {
			t.Errorf("readable file: err = %v", err)
		}
		os.Chmod(path, 0o600)
	}
	os.WriteFile(filepath.Join(filepath.Dir(path), "credentials"), []byte("typesafe_api_key = \""+fakeKey+"\" junk"), 0o600)
	if _, _, err := LoadKey(path); err == nil || strings.Contains(err.Error(), fakeKey) {
		t.Errorf("broken file: err = %v", err)
	}
	if err := DeleteKey(path); err != nil {
		t.Fatal(err)
	}
	if err := DeleteKey(path); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

func TestCheckCall(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("x"), 0o600)
	call := func(tool, input string) Call {
		return Call{Tool: tool, Input: json.RawMessage(input), Cwd: root, Root: root}
	}
	bash := func(cmd string) Call {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return call("Bash", string(b))
	}
	write := func(p string) Call {
		b, _ := json.Marshal(map[string]string{"file_path": p, "content": "x"})
		return call("Write", string(b))
	}
	const ok = "" // on the allowlist, no rule
	const unchecked = "unchecked"
	// Links for the round-2 cases: link to elsewhere, a bare `out` link, a
	// dangling one, .claude as a link to an in-repo dir, and an in-repo
	// link to .claude.
	links := os.Symlink(outside, filepath.Join(root, "link")) == nil
	if links {
		os.Symlink(filepath.Join(outside, "target"), filepath.Join(root, "out"))
		os.Symlink(filepath.Join(outside, "new", "file"), filepath.Join(root, "dangling"))
		os.MkdirAll(filepath.Join(root, "conf"), 0o755)
		os.Symlink(filepath.Join(root, "conf"), filepath.Join(root, ".claude"))
		os.MkdirAll(filepath.Join(root, "real"), 0o755)
		os.Symlink(filepath.Join(root, ".claude"), filepath.Join(root, "cfg"))
	}
	type tc struct {
		name string
		c    Call
		want string // ok, unchecked, or a rule the call must also break
	}
	cases := []tc{
		{"go test", bash("go test ./..."), ok},
		{"go test flags", bash("go test -race -count 1 -run TestX ./internal/..."), ok},
		{"go mod tidy", bash("go mod tidy"), ok},
		{"git status", bash("git status --short"), ok},
		{"git diff", bash("git diff --stat"), ok},
		{"git log", bash("git log --oneline -5"), ok},
		{"git branch list", bash("git branch -a"), ok},
		{"git remote -v", bash("git remote -v"), ok},
		{"ls", bash("ls -la src"), ok},
		{"cat", bash("cat README.md"), ok},
		{"grep", bash("grep -rn TODO src"), ok},
		{"make target", bash("make test"), ok},
		{"npm test", bash("npm test"), ok},
		{"npm run", bash("npm run lint"), ok},
		{"cargo", bash("cargo test --release"), ok},
		{"python -m pytest", bash("python -m pytest -q"), ok},
		{"quoted words", bash(`grep -n "fix the race" src`), ok},
		{"read inside", call("Read", `{"file_path":"`+root+`/src/a.go"}`), ok},
		{"write inside", write(root + "/src/a.go"), ok},
		{"write relative", call("Edit", `{"file_path":"src/a.go","old_string":"a","new_string":"b"}`), ok},
		{"patch inside", call("apply_patch", `{"command":"*** Begin Patch\n*** Update File: src/a.go\n@@\n-a\n+b\n*** End Patch"}`), ok},

		{"./helper", bash("./helper --fix"), unchecked},
		{"absolute program", bash("/usr/bin/ls"), unchecked},
		{"rm is not listed", bash("rm build/x"), unchecked},
		{"rm --rec --fo", bash("rm --rec --fo data"), RuleRmRf},
		{"git config", bash("git config core.sshCommand x"), unchecked},
		{"git alias", bash("git cleanup"), unchecked},
		{"git -c", bash("git -c core.pager=x log"), unchecked},
		{"git -C", bash("git -C sub status"), unchecked},
		{"git push", bash("git push origin main"), unchecked},
		{"git push --fo", bash("git push --fo origin main"), RuleForcePush},
		{"git reset --ha", bash("git reset --ha origin/main"), RuleResetHard},
		{"git branch -D", bash("git branch -D old"), unchecked},
		{"git branch create", bash("git branch newname"), unchecked},
		{"git log --output", bash("git log --output=x"), unchecked},
		{"git diff --ext-diff", bash("git diff --ext-diff"), unchecked},
		{"go test -exec", bash("go test -exec x ./..."), unchecked},
		{"go mod edit", bash("go mod edit -replace x"), unchecked},
		{"go env -w", bash("go env -w GOFLAGS=x"), unchecked},
		{"npm install a package", bash("npm install leftpad"), unchecked},
		{"npm install -g", bash("npm install -g x"), unchecked},
		{"make without target", bash("make"), unchecked},
		{"make -f", bash("make -f evil.mk test"), unchecked},
		{"make variable", bash("make CC=evil test"), unchecked},
		{"find -delete", bash("find . -name x -delete"), unchecked},
		{"sudo", bash("sudo ls"), RuleSudo},
		{"pipe", bash("go test ./... | tee out"), unchecked},
		{"redirect", bash("echo x > src/a"), unchecked},
		{"variable", bash("$CMD x"), unchecked},
		{"newline", bash("ls\nrm x"), unchecked},
		{"cat dotdot", bash("cat ../secret"), unchecked},
		{"cat outside", bash("cat /etc/passwd"), unchecked},
		{"cat .env", bash("cat .env"), RuleSecrets},
		{"cat .git/config", bash("cat .git/config"), unchecked},
		{"read outside", call("Read", `{"file_path":"/usr/share/dict/words"}`), unchecked},
		{"read .env", call("Read", `{"file_path":"`+root+`/.env"}`), RuleSecrets},
		{"write outside", write("/etc/hosts"), unchecked},
		{"write dotdot", write(root + "/src/../../evil"), unchecked},
		{"write .git hook", write(root + "/.git/hooks/pre-commit"), RuleAgentConfig},
		{"write .claude", write(root + "/.claude/settings.json"), RuleAgentConfig},
		{"write .envrc", write(root + "/.envrc"), unchecked},
		{"write no path", call("Write", `{"content":"x"}`), unchecked},
		{"patch outside", call("apply_patch", `{"command":"*** Begin Patch\n*** Add File: /etc/cron.d/x\n+x\n*** End Patch"}`), unchecked},
		{"not a patch", call("apply_patch", `{"command":"rm -rf x"}`), unchecked},
		{"web fetch", call("WebFetch", `{"url":"https://example.com"}`), unchecked},
		{"mcp tool", call("mcp__fs__write_file", `{"path":"x"}`), unchecked},
		{"garbage input", call("Bash", `not json`), unchecked},
	}
	if links {
		cases = append(cases,
			tc{"link/../escape", write(root + "/link/../escape"), unchecked},
			tc{"shell link/../escape", bash("cat link/../escape"), unchecked},
			tc{"cp a out", bash("cp a out"), unchecked},
			tc{"cat bare out link", bash("cat out"), unchecked},
			tc{"write bare out link", write(root + "/out"), unchecked},
			tc{"through link", write(root + "/link/x"), unchecked},
			tc{"dangling", write(root + "/dangling"), unchecked},
			tc{".claude is a link in the repo", write(root + "/.claude/x"), unchecked},
			tc{"link to .claude", write(root + "/cfg/settings.json"), unchecked},
			tc{"inside, no link", write(root + "/real/x"), ok},
		)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckCall(tc.c, nil, nil)
			switch tc.want {
			case ok:
				if !got.CanAllow() {
					t.Errorf("CheckCall = %+v, want allowlisted", got)
				}
			case unchecked:
				if got.Unverified == "" || got.CanAllow() {
					t.Errorf("CheckCall = %+v, want not allowlisted", got)
				}
			default:
				if !contains(got.Rules, tc.want) || got.CanAllow() {
					t.Errorf("CheckCall = %+v, want rule %q", got, tc.want)
				}
			}
		})
	}
	// allow_programs adds bare names; their paths still count.
	extra := []string{"mytool"}
	if got := CheckCall(bash("mytool --fast src"), nil, extra); !got.CanAllow() {
		t.Errorf("allow_programs: %+v", got)
	}
	for _, cmd := range []string{"mytool /etc/x", "mytool --out=/etc/x", "othertool src"} {
		if got := CheckCall(bash(cmd), nil, extra); got.CanAllow() {
			t.Errorf("%q allowed", cmd)
		}
	}
	got := CheckCall(bash("go test ./..."), []string{"", "go test"}, nil)
	if !contains(got.Rules, RuleUserPrefix+"2") || got.CanAllow() {
		t.Errorf("never_allow = %+v", got)
	}
	for _, r := range got.Rules {
		if strings.Contains(r, "go test") {
			t.Errorf("rule label %q shows the user's never_allow text", r)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestAutoVerdict(t *testing.T) {
	p := func(allow, ask, deny float64) map[string]float64 {
		return map[string]float64{Allow: allow, Ask: ask, Deny: deny}
	}
	cases := []struct {
		choice   string
		probs    map[string]float64
		canAllow bool
		want     string
	}{
		{Allow, p(0.96, 0.03, 0.01), true, Allow},
		{Allow, p(0.95, 0.04, 0.01), true, Allow},
		{Allow, p(0.94, 0.05, 0.01), true, ""},
		{Allow, p(0.99, 0.01, 0), false, ""}, // a rule, an unchecked or a cut call
		{Deny, p(0.01, 0.03, 0.96), true, Deny},
		{Deny, p(0.01, 0.03, 0.96), false, Deny}, // deny needs no check
		{Ask, p(0.2, 0.6, 0.2), true, ""},
		{Deny, p(1, 0, 0), true, ""}, // the choice and the numbers disagree
		{Allow, p(0, 0, 1), true, ""},
		{"", nil, true, ""},
	}
	for _, tc := range cases {
		if got := AutoVerdict(tc.choice, tc.probs, tc.canAllow, 0.95, 0.95); got != tc.want {
			t.Errorf("AutoVerdict(%q, %v, %v) = %q, want %q", tc.choice, tc.probs, tc.canAllow, got, tc.want)
		}
	}
}

func TestUrgencyRounds(t *testing.T) {
	for score, want := range map[float64]string{0: "fyi", 0.4: "fyi", 1.2: "later", 2.5: "now", 2.49: "soon", 3: "now"} {
		if got := Urgency(map[string]Answer{"urgency": {Type: Score, Score: score}}); got != want {
			t.Errorf("Urgency(%v) = %q, want %q", score, got, want)
		}
	}
}

// Field names mark secrets in structured values, at any depth, and a
// quoted value may span lines.
func TestRedactStructuredAndMultiline(t *testing.T) {
	v := RedactValue(map[string]any{
		"password": "hunter2-plain",
		"env":      map[string]any{"API_KEY": "plainkeyvalue", "PORT": "8080"},
		"list":     []any{map[string]any{"client_secret": 12345678}},
		"content":  "API_KEY=\"abc\nsecond-line-secret\"\nPORT=1",
		"tail":     "token: 'cut-off-secret",
	})
	b, _ := json.Marshal(v)
	for _, s := range []string{"hunter2-plain", "plainkeyvalue", "12345678", "second-line-secret", "cut-off-secret"} {
		if strings.Contains(string(b), s) {
			t.Errorf("%q survived: %s", s, b)
		}
	}
	for _, keep := range []string{"8080", "PORT=1", `"password"`} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("lost %q: %s", keep, b)
		}
	}
}

// Prepare redacts before it cuts: a key at the cut, or a PEM block cut in
// the middle, never leaks a part; any cut is reported; and the whole state
// fits the budget however many strings it has.
func TestPrepareRedactsThenCuts(t *testing.T) {
	c := &Client{}
	key := "sk-ant-api03-" + strings.Repeat("Z", 40)
	long := strings.Repeat("word ", stringMax/5-3) + key + strings.Repeat(" tail", 1000)
	out, truncated, _ := c.Prepare(map[string]any{"text": long})
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "ZZZZ") || !truncated {
		t.Errorf("key half sent or cut not reported: truncated=%v %s", truncated, b[len(b)-80:])
	}
	pem := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("QUJD", 4000) + "\n-----END PRIVATE KEY-----"
	out, _, _ = c.Prepare(map[string]any{"screen": pem})
	if b, _ := json.Marshal(out); strings.Contains(string(b), "QUJD") {
		t.Errorf("PEM body sent: %.120s", b)
	}
	if _, truncated, _ := c.Prepare(map[string]any{"text": "short"}); truncated {
		t.Error("a short state reported as cut")
	}
	many := map[string]any{}
	for i := range 40 {
		many[fmt.Sprint("k", i)] = strings.Repeat("abc ", 2000)
	}
	out, truncated, _ = c.Prepare(many)
	if size(out) > stateBudget || !truncated {
		t.Errorf("40 strings: size %d, truncated %v", size(out), truncated)
	}
}

// slowModel answers correctly, but only after its deadline.
type slowModel struct{ d time.Duration }

func (s slowModel) Ask(ctx context.Context, r Request) (map[string]Answer, error) {
	time.Sleep(s.d)
	return map[string]Answer{"q": {Type: Noul, Noul: 0.9}}, nil
}

func TestLateAnswerIsNotUsed(t *testing.T) {
	c := &Client{P: slowModel{50 * time.Millisecond}, Timeout: 10 * time.Millisecond, Counts: &Counters{}}
	ans, err := c.Ask(context.Background(), FeatureTest, "", "s", map[string]Question{"q": {Type: Noul, Instructions: "?"}})
	if err == nil || ans != nil {
		t.Fatalf("late answer used: %v %v", ans, err)
	}
}

func TestCredentialsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mode bits")
	}
	t.Setenv(KeyEnv, "")
	dir := filepath.Join(t.TempDir(), "pitwall")
	os.MkdirAll(dir, 0o755)
	os.Chmod(dir, 0o775)
	path := CredentialsPath(dir)
	if err := SaveKey(path, fakeKey); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", fi.Mode().Perm())
	}
	os.Chmod(dir, 0o777)
	if _, _, err := LoadKey(path); err == nil || strings.Contains(err.Error(), fakeKey) {
		t.Errorf("writable dir: err = %v", err)
	}
}

// Prepare counts and cuts every container type, and refuses a state that
// cannot fit at all.
func TestPrepareContainers(t *testing.T) {
	c := &Client{}
	big := map[string]string{}
	for i := range 20 {
		big[fmt.Sprint("k", i)] = strings.Repeat("abcd ", 2000)
	}
	out, truncated, err := c.Prepare(map[string]any{"input": big})
	if err != nil || !truncated || size(out) > stateBudget {
		t.Errorf("map[string]string: size %d, truncated %v, err %v", size(out), truncated, err)
	}
	keys := map[string]any{}
	for i := range 20000 {
		keys[fmt.Sprint("key-number-", i)] = i
	}
	if _, truncated, err := c.Prepare(keys); err == nil || !truncated {
		t.Errorf("a state of keys alone should not fit: truncated %v, err %v", truncated, err)
	}
	if _, err := c.Ask(context.Background(), FeatureTest, "", keys, map[string]Question{"q": {Type: Noul, Instructions: "?"}}); err == nil {
		t.Error("asked with a state over the budget")
	}
}

// Escaped quotes and unfinished quoted flags do not end redaction early.
func TestRedactEscapesAndOpenQuotes(t *testing.T) {
	for _, in := range []string{
		`API_KEY="first\"remainingsecret"`,
		`{"api_key": "first\"remainingsecret"}`,
		"mysql --password 'first\nremainingsecret",
		`curl --token "first\"remainingsecret"`,
	} {
		if out := Redact(in); strings.Contains(out, "remainingsecret") {
			t.Errorf("Redact(%q) = %q", in, out)
		}
	}
}

// A key cut by the stderr or HTTP body limit is dropped whole, never sent
// in part.
func TestCutErrorTextDropsPartialKey(t *testing.T) {
	key := "sk-ant-api03-" + strings.Repeat("Z", 40)
	body := strings.Repeat("x ", (64<<10)/2-4) + key + " tail"
	var l limited
	l.max = 64 << 10
	l.Write([]byte(body))
	if got := Redact(wholeText(l.Bytes(), l.cut)); strings.Contains(got, "ZZZ") || strings.Contains(got, "sk-ant") || !l.cut {
		t.Errorf("stderr prefix leaked part of the key (cut %v): %q", l.cut, got[len(got)-60:])
	}
	f := &fakeJev{handle: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, strings.Repeat("y ", (1<<20)/2-5)+key+" more")
	}}
	_, j := f.start(t)
	_, err := j.Ask(context.Background(), Request{State: "s", Questions: map[string]Question{"q": {Type: Noul, Instructions: "?"}}})
	if err == nil || strings.Contains(err.Error(), "ZZZ") {
		t.Errorf("HTTP body prefix: %v", err)
	}
}

// Probabilities are scaled to sum to 1 before any threshold: a reply that
// sums to 1.01 does not get an allow past 0.95 on the raw number.
func TestProbabilitiesNormalized(t *testing.T) {
	f := &fakeJev{reply: `{"answers":{"verdict":{"type":"choice","choice":"allow","probabilities":{"allow":0.955,"ask":0.055,"deny":0},"confidence":0.9}}}`}
	_, j := f.start(t)
	c := &Client{P: j}
	ans, err := c.Ask(context.Background(), FeatureApprovals, "", "s", ApprovalQuestions())
	if err != nil {
		t.Fatal(err)
	}
	choice, probs := Approval(ans)
	sum := 0.0
	for _, p := range probs {
		sum += p
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("sum %v", sum)
	}
	if v := AutoVerdict(choice, probs, true, 0.95, 0.95); v != "" {
		t.Errorf("allow %.4f passed 0.95: %q", probs[Allow], v)
	}
}
