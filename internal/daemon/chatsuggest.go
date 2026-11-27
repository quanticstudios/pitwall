package daemon

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/agentenv"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// suggestWait bounds one suggestion run; suggestPoll is how often it looks
// whether the agent started working, which stops it. Tests shorten them.
var (
	suggestWait = 90 * time.Second
	suggestPoll = 500 * time.Millisecond
)

// suggestPrompt is T3 Code's prompt suggestion request (Codex's
// thread/sample prompt), after the end of the conversation it predicts.
const suggestPrompt = "Predict the next message the user will send in this conversation. Reply with only that message, written as the user would type it, in under 20 words. Do not use tools. If there is no clear next step, reply with nothing."

// The context a suggestion sees: the newest prompts and replies, oldest
// first, at most suggestItems of them and suggestRunes in all, each cut
// to suggestItemRunes.
const (
	suggestItems     = 6
	suggestRunes     = 8000
	suggestItemRunes = 3000
	suggestMaxRunes  = 300 // a longer answer is no prompt someone would type
)

// suggestJob is one chat's suggestion run for one turn: whoever asks for
// that turn again waits for it and gets its answer, so each turn costs at
// most one run.
type suggestJob struct {
	turn string
	done chan struct{}
	res  proto.ChatSuggestResult
}

// suggestJobs holds each chat's latest suggestJob.
type suggestJobs struct {
	mu   sync.Mutex
	jobs map[string]*suggestJob
}

// suggestRun is what a suggestion for one chat runs with.
type suggestRun struct {
	prov    model.Provider
	dir     string
	env     []string // on top of the daemon's environment, as the pane or chat has it
	snap    session.Snapshot
	busy    func() bool   // the agent works again, or the chat closed
	turn    func() string // the chat's newest finished turn now
	guarded bool
}

// chatSuggest answers m with the next message the user may send, from a
// one-off headless run of the chat's own agent under the pane's or chat's
// config, so it bills the same plan: Claude Code's claude -p on haiku,
// Codex's codex exec on its cheapest listed model at low effort,
// Antigravity's agy -p on its cheapest flash model. It sees the newest
// prompts and replies only, saves no session, runs no tools, hooks or
// MCP servers, and stops when the agent starts working.
func (d *Daemon) chatSuggest(ctx context.Context, m proto.ChatSuggest) proto.ChatSuggestResult {
	out := proto.ChatSuggestResult{Chat: m.Chat, Turn: m.Turn}
	fail := func(code, msg string) proto.ChatSuggestResult {
		out.Code, out.Err = code, msg
		return out
	}
	if d.o.SuggestNext == nil || !d.o.SuggestNext() {
		return fail("off", "suggested prompts are off ([chat] suggest_next)")
	}
	run, err := d.suggestOf(m.Chat, 4*suggestItems)
	if err != nil {
		var ce chatError
		if errors.As(err, &ce) {
			return fail(ce.code, ce.msg)
		}
		return fail("not-found", err.Error())
	}
	switch {
	case run.guarded:
		return fail("guarded", "no suggestions in a guarded tab")
	case run.busy():
		return fail("busy", "the agent is working")
	case m.Turn == "" || run.turn() != m.Turn:
		return fail("stale", "that turn is not the chat's newest finished one")
	}
	if _, ok := suggestCommand(run.prov, run.env, ""); !ok {
		return fail("unsupported", string(run.prov)+" has no headless mode pitwall can ask")
	}
	ctxText := suggestContext(run.snap)
	if ctxText == "" {
		return out // nothing said yet
	}

	d.suggests.mu.Lock()
	if d.suggests.jobs == nil {
		d.suggests.jobs = map[string]*suggestJob{}
	}
	job := d.suggests.jobs[m.Chat]
	if job != nil && job.turn == m.Turn {
		d.suggests.mu.Unlock()
		select {
		case <-job.done:
			r := job.res
			r.Chat, r.Turn = m.Chat, m.Turn
			return r
		case <-ctx.Done():
			return fail("stale", "the window went away")
		}
	}
	job = &suggestJob{turn: m.Turn, done: make(chan struct{})}
	d.suggests.jobs[m.Chat] = job
	d.suggests.mu.Unlock()
	defer close(job.done)

	prompt := "Here is the end of a conversation between a user and a coding agent.\n\n<conversation>\n" + ctxText + "\n</conversation>\n\n" + suggestPrompt
	cmd, _ := suggestCommand(run.prov, run.env, prompt)
	text, err := runSuggest(context.WithoutCancel(ctx), run, cmd)
	switch {
	case errors.Is(err, errSuggestStopped):
		job.res = proto.ChatSuggestResult{Code: "busy", Err: "the agent started working"}
	case err != nil:
		job.res = proto.ChatSuggestResult{Code: "provider", Err: err.Error()}
	case run.busy() || run.turn() != m.Turn:
		job.res = proto.ChatSuggestResult{Code: "stale", Err: "the chat moved on"}
	default:
		job.res = proto.ChatSuggestResult{Text: cleanSuggestion(text)}
	}
	r := job.res
	r.Chat, r.Turn = m.Chat, m.Turn
	return r
}

// errSuggestStopped is a run stopped because the agent started working.
var errSuggestStopped = errors.New("stopped: the agent is working")

// suggestCmd is one suggestion's command line, and what goes on its
// stdin.
type suggestCmd struct {
	name  string
	args  []string
	stdin string
}

// runSuggest runs cmd in run's folder and environment and returns the
// answer it printed. It stops the process when run.busy turns true.
func runSuggest(ctx context.Context, run suggestRun, cmd suggestCmd) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, suggestWait)
	defer cancel()
	c := exec.CommandContext(ctx, cmd.name, cmd.args...)
	c.Dir = run.dir
	c.Env = suggestEnv(run.env)
	c.Stdin = strings.NewReader(cmd.stdin)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	c.WaitDelay = time.Second
	if err := c.Start(); err != nil {
		return "", err
	}
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	stopped := false
	tick := time.NewTicker(suggestPoll)
	defer tick.Stop()
	var err error
wait:
	for {
		select {
		case err = <-waited:
			break wait
		case <-tick.C:
			if !stopped && run.busy() {
				stopped = true
				cancel()
			}
		}
	}
	switch {
	case stopped:
		return "", errSuggestStopped
	case ctx.Err() == context.DeadlineExceeded:
		return "", errors.New("the agent took longer than " + suggestWait.String())
	case err != nil:
		return "", errors.New(cmp.Or(lastLine(stderr.String()), err.Error()))
	}
	out := stdout.String()
	if run.prov == model.ProviderAntigravity {
		out = agyResponse(out, run.env)
	}
	return out, nil
}

// suggestEnv is the daemon's environment without the session markers and
// pitwall's pane variables, with env on top: a hook the one-off agent
// runs then finds no pane and does nothing.
func suggestEnv(env []string) []string {
	out := append(agentenv.Clean(os.Environ()), env...)
	return slices.DeleteFunc(out, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "PITWALL_PANE" || k == "PITWALL_ATTACH" || k == "PITWALL_SOCKET"
	})
}

// suggestCommand is the headless command line that sends prompt to
// prov; false for an agent with no headless mode pitwall drives. env is
// the pane's or chat's.
func suggestCommand(prov model.Provider, env []string, prompt string) (suggestCmd, bool) {
	switch prov {
	case model.ProviderClaude:
		return suggestCmd{cmp.Or(os.Getenv(claude.CommandEnv), "claude"), []string{"-p", "--model", "haiku",
			"--no-session-persistence", "--tools", "", "--strict-mcp-config", "--disable-slash-commands",
			"--settings", `{"disableAllHooks":true}`, "--output-format", "text"}, prompt}, true
	case model.ProviderCodex:
		a := []string{"exec", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only",
			"--disable", "hooks", "--color", "never", "-c", `model_reasoning_effort="low"`}
		if m := codexCheapest(codexHome(env)); m != "" {
			a = append(a, "-m", m)
		}
		return suggestCmd{cmp.Or(os.Getenv("PITWALL_CODEX_COMMAND"), "codex"), append(a, "-"), prompt}, true
	case model.ProviderAntigravity:
		a := []string{"--disable-slash-commands", "--output-format", "json", "--print-timeout", "80s"}
		if m := agyCheapest(); m != "" {
			a = append(a, "--model", m)
		}
		// agy's -p takes the prompt as its value; attached, so a prompt
		// starting with a dash stays the prompt.
		return suggestCmd{cmp.Or(os.Getenv("PITWALL_AGY_COMMAND"), "agy"), append(a, "-p="+prompt), ""}, true
	}
	return suggestCmd{}, false
}

// codexHome is the CODEX_HOME a pane with env reads.
func codexHome(env []string) string {
	get := envGet(env)
	if h := get("CODEX_HOME"); h != "" {
		return h
	}
	if home := get("HOME"); home != "" {
		return filepath.Join(home, ".codex")
	}
	return ""
}

// codexCheapest is the cheapest model Codex lists in home's models cache:
// of those it offers in its picker, the first whose description calls it
// affordable or whose name says mini; "" leaves Codex's own default.
func codexCheapest(home string) string {
	if home == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil {
		return ""
	}
	var c struct {
		Models []struct {
			Slug        string `json:"slug"`
			Visibility  string `json:"visibility"`
			Priority    int    `json:"priority"`
			Description string `json:"description"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	best, bestPri := "", 0
	for _, m := range c.Models {
		cheap := strings.Contains(strings.ToLower(m.Description), "affordable") || strings.Contains(m.Slug, "mini")
		if m.Visibility != "list" || !cheap || !validModel.MatchString(m.Slug) {
			continue
		}
		if best == "" || m.Priority < bestPri {
			best, bestPri = m.Slug, m.Priority
		}
	}
	return best
}

// validModel is a model id safe to pass as one argument.
var validModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,79}$`)

// agyModels lists agy's models once per daemon; tests replace it.
var agyModels = sync.OnceValue(func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, cmp.Or(os.Getenv("PITWALL_AGY_COMMAND"), "agy"), "models")
	c.Env = suggestEnv(nil)
	b, _ := c.Output()
	return string(b)
})

// agyCheapest is the cheapest model `agy models` lists: a flash model at
// low effort, else any flash one; "" leaves agy's own default.
func agyCheapest() string { return cheapestAgy(agyModels()) }

func cheapestAgy(list string) string {
	var flash []string
	for line := range strings.SplitSeq(list, "\n") {
		id, _, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if strings.Contains(id, "flash") && validModel.MatchString(id) {
			flash = append(flash, id)
		}
	}
	for _, id := range flash {
		if strings.HasSuffix(id, "-low") {
			return id
		}
	}
	if len(flash) > 0 {
		return flash[0]
	}
	return ""
}

// agyResponse is the reply in agy's --output-format json, and it deletes
// the conversation agy saved for the run, which would otherwise join the
// user's history: only the files named by its id, under the HOME env
// gives it.
func agyResponse(out string, env []string) string {
	var r struct {
		ID       string `json:"conversation_id"`
		Response string `json:"response"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &r) != nil {
		return ""
	}
	if uuidRe.MatchString(r.ID) {
		if home := envGet(env)("HOME"); filepath.IsAbs(home) {
			dir := filepath.Join(home, ".gemini", "antigravity-cli")
			os.Remove(filepath.Join(dir, "conversations", r.ID+".db"))
			os.RemoveAll(filepath.Join(dir, "brain", r.ID))
		}
	}
	return r.Response
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cleanSuggestion is s as a prompt to offer: one line, without quotes the
// model wrapped it in; "" for none, a refusal to guess, or one too long
// to be a typed message.
func cleanSuggestion(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for len(s) >= 2 {
		f, l := s[0], s[len(s)-1]
		if (f == '"' && l == '"') || (f == '\'' && l == '\'') || (f == '`' && l == '`') {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	switch strings.ToLower(strings.Trim(s, ".()[] ")) {
	case "", "nothing", "none", "n/a", "no clear next step", "empty":
		return ""
	}
	if utf8.RuneCountInString(s) > suggestMaxRunes {
		return ""
	}
	return s
}

// suggestContext is the end of snap's conversation as the suggestion sees
// it: "User:" and "Assistant:" blocks, oldest first.
func suggestContext(snap session.Snapshot) string {
	var parts []string
	total := 0
	for i := len(snap.Content) - 1; i >= 0 && len(parts) < suggestItems && total < suggestRunes; i-- {
		it := snap.Content[i]
		var who string
		switch it.Kind {
		case session.KindUserPrompt:
			who = "User"
		case session.KindAssistantText:
			who = "Assistant"
		default:
			continue
		}
		t := strings.TrimSpace(it.Text)
		if t == "" {
			continue
		}
		r := []rune(t)
		if n := min(suggestItemRunes, suggestRunes-total); len(r) > n {
			if who == "User" {
				r = append(r[:n], '…')
			} else {
				r = append([]rune{'…'}, r[len(r)-n:]...) // a reply's end says what is left to do
			}
		}
		total += len(r)
		parts = append(parts, who+": "+string(r))
	}
	slices.Reverse(parts)
	return strings.Join(parts, "\n\n")
}

// chatWorking reports whether a chat in state has a turn running or
// waiting on someone.
func chatWorking(state string) bool {
	return state == session.StateRunning || state == session.StateWaiting || state == session.StateConnecting
}

// lastTurn is snap's newest turn's id when it completed, "" while one
// runs or none did. (Snapshot.Running says the agent's process runs.)
func lastTurn(snap session.Snapshot) string {
	if len(snap.Turns) == 0 {
		return ""
	}
	t := snap.Turns[len(snap.Turns)-1]
	if t.State != session.TurnCompleted {
		return ""
	}
	return t.ID
}

// suggestOf is what a suggestion for chat id runs with: a pane chat's
// agent, transcript (its newest tail items) and pane environment, or a
// New task chat's.
func (d *Daemon) suggestOf(id string, tail int) (suggestRun, error) {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	var p model.Pane
	if i >= 0 {
		p = d.st.Panes[i]
	}
	d.mu.Unlock()
	if i >= 0 && model.PaneChat(p) {
		pc, _, ok := d.paneChatOf(id)
		if !ok {
			return suggestRun{}, chatError{"not-found", "pane " + id + " runs no chat"}
		}
		snapOf := func() session.Snapshot {
			pc.mu.Lock()
			defer pc.mu.Unlock()
			s := pc.snap
			s.Content = slices.Clone(s.Content[max(0, len(s.Content)-tail):])
			s.Turns = slices.Clone(s.Turns)
			return s
		}
		paneBusy := func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.panes[id] == nil {
				return true
			}
			ai := d.activityIndex(id)
			return ai >= 0 && busy(d.st.Activities[ai].State)
		}
		return suggestRun{prov: p.Provider, dir: p.Cwd, env: slices.Clone(p.Env), snap: snapOf(),
			busy:    paneBusy,
			turn:    func() string { return lastTurn(snapOf()) },
			guarded: envGet(p.Env)(guard.Env) != ""}, nil
	}
	c, err := d.chat(id)
	if err != nil {
		return suggestRun{}, err
	}
	var run suggestRun
	if !c.do(func() {
		s := c.snap
		s.Content = slices.Clone(s.Content[max(0, len(s.Content)-tail):])
		s.Turns = slices.Clone(s.Turns)
		run = suggestRun{prov: c.cfg.Provider, dir: c.cfg.Cwd, env: slices.Clone(c.cfg.Env), snap: s,
			guarded: envGet(c.cfg.Env)(guard.Env) != ""}
	}) {
		return suggestRun{}, chatError{"not-found", "chat " + id + " is closed"}
	}
	run.busy = func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		i := slices.IndexFunc(d.st.Chats, func(m model.Chat) bool { return m.ID == id })
		return i < 0 || chatWorking(d.st.Chats[i].State)
	}
	run.turn = func() string {
		var t string
		c.run(func() { t = lastTurn(c.snap) })
		return t
	}
	return run, nil
}
