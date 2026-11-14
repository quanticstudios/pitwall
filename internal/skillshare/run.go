package skillshare

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
)

// Result is a finished command: its output and exit code, -1 when it
// could not start.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Client runs skillshare and reads the files a preview shows. Local runs
// the CLI on this machine; Demo answers from captured output.
type Client struct {
	// Run runs skillshare with args in dir, "" for the home folder.
	Run func(ctx context.Context, dir string, args ...string) Result
	// Clone makes a shallow clone of url's branch ("" for its default)
	// in dest, for the audit before an install.
	Clone func(ctx context.Context, url, branch, dest string) error
	// FS reads the source's files by absolute path.
	FS func(path string) ([]byte, error)
	// Dir lists a skill's files, relative to dir.
	Dir func(dir string) ([]string, error)
	// Installed reports whether skillshare is on PATH.
	Installed func() bool
}

// Default is the client pitwall uses: Local, or Demo in pwdemo-app.
var Default = Local()

// Local is the CLI on this machine's PATH.
func Local() Client {
	return Client{Run: execRun, Clone: gitClone, FS: readCapped, Dir: listFiles, Installed: onPath}
}

func execRun(ctx context.Context, dir string, args ...string) Result {
	cmd := exec.CommandContext(ctx, "skillshare", args...)
	cmd.Dir = dir
	if dir == "" {
		cmd.Dir, _ = os.UserHomeDir()
	}
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		return Result{Stdout: out.String(), Stderr: err.Error(), Code: -1}
	}
	return Result{out.String(), errb.String(), code}
}

func gitClone(ctx context.Context, url, branch, dest string) error {
	args := []string{"clone", "-q", "--depth", "1"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	cmd := gitstat.Command(ctx, filepath.Dir(dest), append(args, "--", url, dest)...)
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(cmp.Or(LastLine(string(out)), err.Error()))
	}
	return nil
}

// previewCap is how much of a file a preview reads.
const previewCap = 60_000

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, previewCap))
}

// listFiles is a skill's files, .git left out, 40 at most.
func listFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && len(out) < 40 {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

var (
	pathAt  atomic.Int64
	pathHas atomic.Bool
)

// onPath looks skillshare up at most every 30 seconds, so a frame can ask.
func onPath() bool {
	now := time.Now().Unix()
	if at := pathAt.Load(); at == 0 || now-at >= 30 {
		pathAt.Store(now)
		_, err := exec.LookPath("skillshare")
		pathHas.Store(err == nil)
	}
	return pathHas.Load()
}

// FindProject is dir when skillshare treats it as a project: it holds
// .skillshare/config.yaml or skillshare/config.yaml. "" otherwise.
func FindProject(dir string) string {
	if dir == "" {
		return ""
	}
	for _, f := range []string{".skillshare/config.yaml", "skillshare/config.yaml"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil && !st.IsDir() {
			return dir
		}
	}
	return ""
}

// Store keeps the last answers of every read, per scope, for the UI
// goroutine to draw while the reads run in their own goroutines.
type Store struct {
	mu         sync.Mutex
	project    string           // the project folder read, "" for none
	reads      map[string]*Read // by scope
	gen        int              // bumped on every change
	running    bool             // a refresh's reads are out
	runID      int              // identity of the current refresh
	again      func()           // a refresh requested while reads were out
	againForce bool             // a queued update check must survive a later periodic read
	checked    time.Time        // when the update checks last ran

	items    []Item // built from reads at itemsGen
	itemsGen int
}

// View is what the popup draws, taken under the store's lock.
type View struct {
	Project string
	Reads   map[string]*Read
	Items   []Item
	Version string
	Gen     int
}

// View is the store's answers now. Reads are shared: read them, do not
// change them.
func (s *Store) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil || s.itemsGen != s.gen {
		s.items = []Item{}
		for _, scope := range sortedKeys(s.reads) {
			s.items = append(s.items, BuildItems(scope, s.reads[scope])...)
		}
		s.itemsGen = s.gen
	}
	v := View{Project: s.project, Reads: map[string]*Read{}, Items: s.items, Gen: s.gen}
	for k, r := range s.reads {
		c := *r
		c.Pending, c.Errors = clone(r.Pending), clone(r.Errors)
		v.Reads[k] = &c
	}
	if r := s.reads[Global]; r != nil && r.Status != nil {
		v.Version = r.Status.Version
	}
	return v
}

func clone[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Running reports whether a refresh is out.
func (s *Store) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// checkEvery is how often a refresh also asks every source's remote for
// updates.
const checkEvery = 10 * time.Minute

// Refresh reads every scope of project ("" for global only) again, each
// read in its own goroutine, keeping the old answers until new ones land;
// changed is called after each lands. A request during a refresh queues
// another read; changing projects replaces the active view immediately.
// The checks run too when force is set or checkEvery has passed.
func (s *Store) Refresh(c Client, project string, force bool, changed func()) {
	s.refresh(c, project, force, changed, 0)
}

func (s *Store) refresh(c Client, project string, force bool, changed func(), after int) {
	s.mu.Lock()
	if after != 0 && s.runID != after {
		s.mu.Unlock()
		return
	}
	if s.running && s.project == project {
		id := s.runID
		force = force || s.againForce
		s.againForce = force
		s.again = func() { s.refresh(c, project, force, changed, id) }
		s.mu.Unlock()
		return
	}
	s.running = true
	s.runID++
	runID := s.runID
	s.again, s.againForce = nil, false
	if s.reads == nil || s.project != project {
		s.reads = map[string]*Read{}
		s.checked = time.Time{}
	}
	s.project = project
	scopes := []string{Global}
	if project != "" {
		scopes = append(scopes, Project)
	}
	checks := force || time.Since(s.checked) >= checkEvery
	if checks {
		s.checked = time.Now()
	}
	type job struct {
		scope, name string
		args        []string
	}
	var jobs []job
	for _, scope := range scopes {
		r := s.reads[scope]
		if r == nil {
			r = &Read{}
			s.reads[scope] = r
		}
		r.Pending, r.Errors = map[string]bool{}, map[string]string{}
		for _, name := range ReadNames {
			r.Pending[name] = true
			jobs = append(jobs, job{scope, name, Reads[name]})
		}
		if checks {
			for name, args := range Checks {
				jobs = append(jobs, job{scope, name, args})
			}
		}
	}
	s.gen++
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			dir := ""
			if j.scope == Project {
				dir = project
			}
			res := c.Run(ctx, dir, append(slices.Clone(j.args), "--json", Flag(j.scope))...)
			s.mu.Lock()
			if s.runID == runID {
				r := s.reads[j.scope]
				fresh := &Read{}
				err := fresh.Parse(j.name, res)
				if err != nil {
					r.Errors[j.name] = err.Error()
				} else {
					mergeRead(r, fresh, j.name)
				}
				delete(r.Pending, j.name)
				s.gen++
			}
			s.mu.Unlock()
			changed()
		}()
	}
	go func() {
		wg.Wait()
		s.mu.Lock()
		var again func()
		if s.runID == runID {
			s.running = false
			again, s.again = s.again, nil
		}
		s.mu.Unlock()
		if again != nil {
			again()
		}
		changed()
	}()
}

// mergeRead copies read name's answer from src into dst.
func mergeRead(dst, src *Read, name string) {
	switch name {
	case "list":
		dst.List = src.List
	case "diff":
		dst.Diff = src.Diff
	case "mcp":
		dst.MCP = src.MCP
	case "plugins":
		dst.Plugins = src.Plugins
	case "hooks":
		dst.Hooks = src.Hooks
	case "status":
		dst.Status = src.Status
	case "check":
		dst.Check = src.Check
	case "checkAgents":
		dst.CheckAgents = src.CheckAgents
	}
}

// Preview is what a row's preview shows beyond the row: a skill's files
// and SKILL.md, an agent's file, or an MCP server's settings.
type Preview struct {
	Path  string   // the skill's folder or the agent's file
	Files []string // a skill's files
	Meta  []KV     // the document's frontmatter
	Body  string   // the document after it
	MCP   []KV     // an MCP server's settings, secrets masked
	Err   string
}

// KV is one row of settings or frontmatter.
type KV struct{ Key, Value string }

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/loader.js

// LoadPreview reads the preview of it from the source r names.
func (c Client) LoadPreview(it Item, r *Read) Preview {
	switch it.Kind {
	case MCP:
		if r == nil || r.MCP == nil || r.MCP.SourcePath == "" {
			return Preview{Err: "skillshare did not say where the MCP source is."}
		}
		b, err := c.FS(r.MCP.SourcePath)
		if err != nil {
			return Preview{Err: err.Error()}
		}
		rows := MCPServer(string(b), it.Name)
		if rows == nil {
			return Preview{Err: it.Name + " is not in " + r.MCP.SourcePath}
		}
		return Preview{Path: r.MCP.SourcePath, MCP: rows}
	case Skills, Agents:
	default:
		return Preview{}
	}
	if r == nil || r.Status == nil {
		return Preview{Err: "skillshare did not say where this source is."}
	}
	if it.Kind == Agents {
		if r.Status.Agents.Source == "" {
			return Preview{Err: "skillshare did not say where the agents are."}
		}
		p := filepath.Join(r.Status.Agents.Source, it.Rel+".md")
		b, err := c.FS(p)
		if err != nil {
			return Preview{Path: p, Err: it.Rel + ".md could not be read."}
		}
		meta, body := SplitFrontmatter(string(b))
		return Preview{Path: p, Files: []string{it.Label + ".md"}, Meta: meta, Body: body}
	}
	dir := filepath.Join(r.Status.Source.Path, it.Rel)
	files, _ := c.Dir(dir)
	b, err := c.FS(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Preview{Path: dir, Files: files, Err: dir + " has no SKILL.md that could be read."}
	}
	if !slices.Contains(files, "SKILL.md") {
		files = append([]string{"SKILL.md"}, files...)
	}
	meta, body := SplitFrontmatter(string(b))
	return Preview{Path: dir, Files: files, Meta: meta, Body: body}
}

// TempDir is where an install's audit clone goes: a folder named so a
// leftover one is easy to tell apart. Tests replace it.
var TempDir = func() (string, error) { return os.MkdirTemp("", "pitwall-skillshare-*") }

// Plan is an install's discovery: the audit clone and the CLI's exact
// install plan.
type Plan struct {
	Dir     string   // the clone's parent folder, removed by Drop
	Choices []Choice // the skills the CLI would install
	Err     string
}

// Choice is one skill a repository offers, by the CLI's name and its path
// in the repository.
type Choice struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Discover clones source for the audit and asks the CLI what it would
// install, in scope (cwd is the project for Project).
func (c Client) Discover(ctx context.Context, source, scope, cwd string) Plan {
	target, ok := CloneURL(source)
	if !ok {
		return Plan{Err: "That doesn't look like a repository: try owner/repo or a git URL."}
	}
	dir, err := TempDir()
	if err != nil {
		return Plan{Err: err.Error()}
	}
	if err := c.Clone(ctx, target.URL, target.Branch, filepath.Join(dir, "r")); err != nil {
		os.RemoveAll(dir)
		return Plan{Err: "Could not read the repository: " + err.Error()}
	}
	res := c.Run(ctx, cwd, "install", source, "--dry-run", "--json", "--kind", "skill", Flag(scope))
	p := ParseDiscover(res)
	p.Dir = dir
	if p.Err == "" && len(p.Choices) == 0 {
		p.Err = "No installable skills in this repository."
	}
	if p.Err != "" {
		Drop(dir)
	}
	return p
}

// Drop removes an audit clone's folder.
func Drop(dir string) {
	if dir != "" && strings.Contains(filepath.Base(dir), "pitwall-skillshare-") {
		os.RemoveAll(dir)
	}
}

// Audit runs skillshare audit on each picked skill of the clone in dir.
func (c Client) Audit(ctx context.Context, dir, scope, cwd string, picks []Choice) AuditResult {
	var parts []AuditPart
	for _, p := range picks {
		res := c.Run(ctx, cwd, "audit", filepath.Join(dir, "r", filepath.FromSlash(p.Path)), "--json", Flag(scope))
		parts = append(parts, AuditPart{Folder: p.Path, Stdout: res.Stdout})
	}
	return ParseAudit(parts, filepath.ToSlash(filepath.Join(dir, "r")))
}

// Install installs skills (all of the plan for nil) from source; force
// installs past the audit.
func (c Client) Install(ctx context.Context, source, scope, cwd string, skills []string, force bool) (done []string, errText string) {
	args := []string{"install", source, "--json", "--kind", "skill", Flag(scope)}
	if len(skills) > 0 {
		args = append(args, "--skill", strings.Join(skills, ","))
	}
	if force {
		args = append(args, "--force")
	}
	return InstallResult(c.Run(ctx, cwd, args...))
}
