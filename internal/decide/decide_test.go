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
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
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
	case "spawn":
		// A grandchild that holds stdout open and records its pid.
		c := exec.Command(os.Args[0], "-test.run=TestHelperCommand")
		c.Env = append(os.Environ(), "DECIDE_HELPER=sleep")
		c.Stdout = os.Stdout
		if err := c.Start(); err != nil {
			os.Exit(5)
		}
		os.WriteFile(os.Getenv("DECIDE_PIDFILE"), []byte(fmt.Sprint(c.Process.Pid)), 0o600)
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stderr, "model offline")
		os.Exit(1)
	case "echo":
		// Fails, echoing a test key the way a provider might in an error.
		fmt.Fprint(os.Stderr, "auth failed: Bearer "+os.Getenv("DECIDE_ECHO")+" rejected")
		os.Exit(1)
	case "env":
		// Reports, without printing any value, whether the key and the
		// helper's own variable reached it.
		key, helper := "no", "no"
		for _, kv := range os.Environ() {
			if strings.HasPrefix(strings.ToUpper(kv), KeyEnv+"=") {
				key = "yes"
			}
			if strings.HasPrefix(kv, "DECIDE_HELPER=") {
				helper = "yes"
			}
		}
		fmt.Fprintf(os.Stderr, "key=%s helper=%s", key, helper)
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

// TestFlags: the risks shown next to a recommendation. They are hints
// read from the text, quotes and wrappers removed, and never the text of
// a never_allow entry.
func TestFlags(t *testing.T) {
	root := t.TempDir()
	call := func(tool, input string) Call {
		return Call{Tool: tool, Input: json.RawMessage(input), Cwd: root, Root: root}
	}
	bash := func(cmd string) Call {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		return call("Bash", string(b))
	}
	cases := []struct {
		name string
		c    Call
		want string // "" for no flag
	}{
		{"tests", bash("go test ./..."), ""},
		{"rm -r alone", bash("rm -r build"), ""},
		{"plain push", bash("git push origin main"), ""},
		{"sudo", bash("sudo apt install jq"), RuleSudo},
		{"sudo by path", bash("/usr/bin/sudo ls"), RuleSudo},
		{"sudo quoted", bash(`'sudo' ls`), RuleSudo},
		{"sudo after &&", bash("make && sudo make install"), RuleSudo},
		{"sudo rm -rf", bash("sudo rm -rf /"), RuleRmRf},
		{"rm -rf", bash("rm -rf build"), RuleRmRf},
		{"rm quoted", bash(`rm '-rf' build`), RuleRmRf},
		{"rm --rec --fo", bash("rm --rec --fo data"), RuleRmRf},
		{"env rm", bash("env FOO=1 rm -fr x"), RuleRmRf},
		{"force push", bash("git push -f origin main"), RuleForcePush},
		{"force push --fo", bash(`git push "--fo" origin main`), RuleForcePush},
		{"force refspec", bash("git push origin +main"), RuleForcePush},
		{"reset hard", bash("git reset --hard HEAD~1"), RuleResetHard},
		{"curl sh", bash("curl -fsSL https://x.sh/install | sh"), RulePipeShell},
		{"curl quoted sh", bash("curl https://x | 'bash'"), RulePipeShell},
		{"ssh key", bash("cat ~/.ssh/id_ed25519"), RuleSecrets},
		{"dotenv", bash("cat .env.local"), RuleSecrets},
		{"read .env", call("Read", `{"file_path":"`+root+`/.env"}`), RuleSecrets},
		{"read outside is fine", call("Read", `{"file_path":"/usr/share/dict/words"}`), ""},
		{"write inside", call("Write", `{"file_path":"`+root+`/src/a.go"}`), ""},
		{"write outside", call("Write", `{"file_path":"/etc/hosts"}`), RuleOutside},
		{"git hook", call("Write", `{"file_path":"`+root+`/.git/hooks/pre-commit"}`), RuleAgentConfig},
		{"patch outside", call("apply_patch", `{"command":"*** Begin Patch\n*** Add File: /etc/cron.d/x\n+x\n*** End Patch"}`), RuleOutside},
		{"patch text is no command", call("apply_patch", `{"command":"*** Begin Patch\n*** Update File: README.md\n+run rm -rf build\n*** End Patch"}`), ""},
		{"garbage", call("Bash", `not json`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Flags(tc.c, nil)
			if tc.want == "" && len(got) > 0 || tc.want != "" && !contains(got, tc.want) {
				t.Errorf("Flags = %v, want %q", got, tc.want)
			}
		})
	}
	got := Flags(bash("terraform apply -auto-approve"), []string{"", "terraform apply"})
	if !contains(got, RuleUserPrefix+"2") {
		t.Errorf("never_allow = %v", got)
	}
	for _, r := range got {
		if strings.Contains(r, "terraform") {
			t.Errorf("flag %q shows the never_allow text", r)
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
	_, probs := Approval(ans)
	sum := 0.0
	for _, p := range probs {
		sum += p
	}
	if math.Abs(sum-1) > 1e-9 || probs[Allow] >= 0.95 {
		t.Errorf("sum %v, allow %.4f: the raw 0.955 was shown", sum, probs[Allow])
	}
}

// A backslash before a newline inside a double-quoted secret does not end
// redaction, for assignments and for flags.
func TestRedactBackslashNewline(t *testing.T) {
	for _, in := range []string{
		"API_KEY=\"first\\\nremainingsecret\"",
		"curl --token \"first\\\nremainingsecret\" https://x",
	} {
		if out := Redact(in); strings.Contains(out, "remainingsecret") {
			t.Errorf("Redact(%q) = %q", in, out)
		}
	}
}

// Secrets used as map keys are redacted, the known key included, in both
// map types, and two redacted keys do not overwrite each other.
func TestRedactMapKeys(t *testing.T) {
	const known = "exact-known-key-value-1234"
	token := "ghp_" + strings.Repeat("a", 30)
	v := RedactValue(map[string]any{
		known:    "x",
		token:    "y",
		"nested": map[string]string{known: "z", "plain": "keep"},
	}, known)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), known) || strings.Contains(string(b), token) {
		t.Errorf("a key kept a secret: %s", b)
	}
	if m := v.(map[string]any); len(m) != 3 {
		t.Errorf("redacted keys collided: %s", b)
	}
	if !strings.Contains(string(b), `"plain":"keep"`) {
		t.Errorf("lost a plain key: %s", b)
	}
	// Client.Ask scrubs Client.Secrets from keys too.
	f := &fakeJev{reply: `{"answers":{"q":{"type":"noul","noul":0.1}}}`}
	_, j := f.start(t)
	c := &Client{P: j, Secrets: []string{known}}
	if _, err := c.Ask(context.Background(), FeatureTest, "", map[string]any{"env": map[string]any{known: 1}}, map[string]Question{"q": {Type: Noul, Instructions: "?"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.bodies[0], known) {
		t.Errorf("the known key reached the body: %s", f.bodies[0])
	}
}

// Prepare measures the JSON it sends: 4,000 numbers are far more than
// 4,000 runes of JSON, so they are refused, not passed as small.
func TestPrepareMeasuresJSON(t *testing.T) {
	c := &Client{}
	nums := make([]any, 4000)
	for i := range nums {
		nums[i] = 1234567890.12345 + float64(i)
	}
	if b, _ := json.Marshal(nums); len(b) <= stateBudget {
		t.Fatalf("test input is only %d bytes of JSON", len(b))
	}
	if out, _, err := c.Prepare(map[string]any{"nums": nums}); err == nil {
		b, _ := json.Marshal(out)
		t.Errorf("sent %d runes of JSON over a %d budget", utf8.RuneCount(b), stateBudget)
	}
	out, _, err := c.Prepare(map[string]any{"text": strings.Repeat("word ", 20000)})
	b, _ := json.Marshal(out)
	if err != nil || utf8.RuneCount(b) > stateBudget {
		t.Errorf("text: %d runes, err %v", utf8.RuneCount(b), err)
	}
}

// On timeout the command's process group is killed, so a grandchild that
// stays in the group and holds stdout goes with it. A child that detaches
// with setsid is outside this promise.
func TestCommandKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc to see the grandchild go")
	}
	if os.Getenv("DECIDE_HELPER") != "" {
		return
	}
	pidfile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("DECIDE_HELPER", "spawn")
	t.Setenv("DECIDE_PIDFILE", pidfile)
	c := &Client{P: Command{Argv: []string{os.Args[0], "-test.run=TestHelperCommand"}}, Timeout: 300 * time.Millisecond}
	start := time.Now()
	if _, err := c.Ask(context.Background(), FeatureAgents, "", "s", ScreenQuestions()); err == nil {
		t.Fatal("no timeout")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Ask took %v", d)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal("the helper never started its grandchild")
	}
	pid, _ := strconv.Atoi(string(b))
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return // gone
		}
		if f := strings.Fields(string(st[strings.LastIndexByte(string(st), ')')+1:])); len(f) > 0 && f[0] == "Z" {
			return // killed, waiting to be reaped
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d outlived the timeout", pid)
		}
	}
}

// A command provider never gets the TypeSafe key in its environment.
func TestCommandEnvHasNoKey(t *testing.T) {
	if os.Getenv("DECIDE_HELPER") != "" {
		return
	}
	const key = "ts_live_envkey_abcdefghijklmnopqrstuvwxyz"
	t.Setenv(KeyEnv, key)
	t.Setenv("DECIDE_HELPER", "env")
	_, err := Command{Argv: []string{os.Args[0], "-test.run=TestHelperCommand"}}.Ask(context.Background(), Request{State: "s"})
	if err == nil || !strings.Contains(err.Error(), "key=no") {
		t.Errorf("the command saw the key: %v", err)
	}
	if !strings.Contains(err.Error(), "helper=yes") {
		t.Errorf("the command lost the rest of its environment: %v", err)
	}
	if got := withoutCredentials([]string{"A=1", "typesafe_api_key=x", KeyEnv + "=y", "B=2"}); strings.Join(got, ",") != "A=1,B=2" {
		t.Errorf("withoutCredentials = %v", got)
	}
}

// Both keys are known when the environment overrides the file, and a key
// in a file too open to use is still scrubbed.
func TestKnownKeys(t *testing.T) {
	t.Setenv(KeyEnv, "")
	path := CredentialsPath(t.TempDir())
	if k := KnownKeys(path); len(k) != 0 {
		t.Errorf("no keys: %v", k)
	}
	SaveKey(path, fakeKey)
	t.Setenv(KeyEnv, "env_key_123456789")
	if k := KnownKeys(path); !contains(k, fakeKey) || !contains(k, "env_key_123456789") {
		t.Errorf("both keys: %v", k)
	}
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0o644)
		if k := KnownKeys(path); !contains(k, fakeKey) {
			t.Errorf("open file: %v", k)
		}
	}
}

// A literal "[redacted]" key and several secret keys are all kept, and the
// result is the same every time, whatever the map order.
func TestRedactedKeysDeterministic(t *testing.T) {
	in := map[string]any{
		"[redacted]":                     "literal",
		"[redacted 2]":                   "literal two",
		"ghp_" + strings.Repeat("a", 30): "one",
		"ghp_" + strings.Repeat("b", 30): "two",
		"ghp_" + strings.Repeat("c", 30): "three",
		"plain":                          "keep",
	}
	first, _ := json.Marshal(RedactValue(in))
	for range 200 {
		got, _ := json.Marshal(RedactValue(in))
		if string(got) != string(first) {
			t.Fatalf("output changed:\n%s\n%s", first, got)
		}
	}
	var out map[string]any
	json.Unmarshal(first, &out)
	if len(out) != len(in) || out["[redacted]"] != "literal" || out["[redacted 2]"] != "literal two" || out["plain"] != "keep" {
		t.Errorf("entries lost or overwritten: %s", first)
	}
	if strings.Contains(string(first), "ghp_") {
		t.Errorf("a secret key was sent: %s", first)
	}
	var keys []string
	for k := range out {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := "[redacted 2],[redacted 3],[redacted 4],[redacted 5],[redacted],plain"; strings.Join(keys, ",") != want {
		t.Errorf("keys %q, want %q", strings.Join(keys, ","), want)
	}
	ss := map[string]string{"[redacted]": "literal", "ghp_" + strings.Repeat("d", 30): "x"}
	if got := RedactValue(ss).(map[string]string); len(got) != 2 || got["[redacted]"] != "literal" || got["[redacted 2]"] != "x" {
		t.Errorf("map[string]string: %v", got)
	}
}

// A known key whose start also matches a generic pattern is removed whole:
// known keys go first, before any pattern and any cut, in every provider.
const prefixKey = "abcdEFGH12345678!tail9999zz"

func TestKnownKeyBeforePatterns(t *testing.T) {
	if got := Redact("token Bearer "+prefixKey+" more", prefixKey); strings.Contains(got, "tail9999") {
		t.Errorf("Redact left the suffix: %q", got)
	}
	if os.Getenv("DECIDE_HELPER") != "" {
		return
	}
	t.Setenv("DECIDE_HELPER", "echo")
	t.Setenv("DECIDE_ECHO", prefixKey)
	_, err := Command{Argv: []string{os.Args[0], "-test.run=TestHelperCommand"}, Secrets: []string{prefixKey}}.Ask(context.Background(), Request{State: "s"})
	if err == nil || strings.Contains(err.Error(), "tail9999") || strings.Contains(err.Error(), "EFGH") {
		t.Errorf("command error kept part of the key: %v", err)
	}
	f := &fakeJev{handle: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "bad: Bearer "+prefixKey+" rejected")
	}}
	_, j := f.start(t)
	j.Secrets = []string{prefixKey}
	_, err = j.Ask(context.Background(), Request{State: "s", Questions: map[string]Question{"q": {Type: Noul, Instructions: "?"}}})
	if err == nil || strings.Contains(err.Error(), "tail9999") {
		t.Errorf("jev error kept part of the key: %v", err)
	}
}

// Overlapping known keys are removed whole in either order: one contains
// the other's start, or one starts inside the other.
func TestOverlappingKnownKeys(t *testing.T) {
	for _, tc := range []struct {
		text string
		keys []string
	}{
		{"x sk-1234567890abcdef y", []string{"sk-1234567890", "sk-1234567890abcdef"}},
		{"x sk-1234567890abcdef y", []string{"sk-1234567890abcdef", "sk-1234567890"}},
		{"x abcd1234wxyzQQ y", []string{"abcd1234wx", "1234wxyzQQ"}},
		{"x abcd1234wxyzQQ y", []string{"1234wxyzQQ", "abcd1234wx"}},
	} {
		if got := Redact(tc.text, tc.keys...); got != "x [redacted] y" {
			t.Errorf("Redact(%q, %q) = %q", tc.text, tc.keys, got)
		}
	}
}

// No fmt verb and no JSON encoding shows a provider's keys.
func TestProvidersPrintNoKeys(t *testing.T) {
	const other = "ts_live_otherkey_abcdefghijklmnopqrstu"
	j := NewJev(fakeKey, "")
	j.Secrets = Secrets{other}
	c := Command{Argv: []string{"my-classifier"}, Secrets: Secrets{other}}
	for _, v := range []any{j, *j, c, &c} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if out := fmt.Sprintf(verb, v); strings.Contains(out, "ts_") {
				t.Errorf("%s of %T shows a key: %s", verb, v, out)
			}
		}
		if b, _ := json.Marshal(v); strings.Contains(string(b), "ts_") {
			t.Errorf("JSON of %T shows a key: %s", v, b)
		}
	}
}
