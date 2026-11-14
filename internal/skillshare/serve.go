package skillshare

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The operations a Call names.
const (
	OpRead      = "read"      // one of Reads or Checks: Name, Scope, Dir
	OpPreview   = "preview"   // Item, with the sources Paths names
	OpSync      = "sync"      // Scopes, Plugins, Dir
	OpUpdate    = "update"    // Items, All, Dir
	OpToggle    = "toggle"    // Item, Action, Dir
	OpUninstall = "uninstall" // Item, Dir
	OpDiscover  = "discover"  // Source, Scope, Dir
	OpAudit     = "audit"     // Clone, Scope, Dir, Picks
	OpInstall   = "install"   // Source, Scope, Dir, Skills, Force
	OpDrop      = "drop"      // Clone
	OpMachine   = "machine"   // a Machines card
	OpPull      = "pull"      // skillshare pull, which syncs too
	OpBothWays  = "bothways"  // skillshare push --pull
	OpUpgrade   = "upgrade"   // skillshare upgrade --cli
	OpProject   = "project"   // Dir, when skillshare treats it as a project
)

// Call is one piece of the popup's work, done by Serve on the machine
// whose daemon gets it. Dir is the project folder, "" for none.
type Call struct {
	Op                  string
	Name, Scope, Dir    string
	Item                Item
	Items               []Item
	Scopes              []string
	Action              string // toggle: "enable" or "disable"
	All, Plugins, Force bool
	Source              string   // discover, install
	Clone               string   // audit, drop: Plan.Dir
	Picks               []Choice // audit
	Skills              []string // install
	Paths               Paths    // preview
}

// Paths are where a scope's Read says its sources are, for a preview.
type Paths struct{ MCP, Skills, Agents string }

// PathsOf is r's source paths.
func PathsOf(r *Read) Paths {
	var p Paths
	if r == nil {
		return p
	}
	if r.MCP != nil {
		p.MCP = r.MCP.SourcePath
	}
	if r.Status != nil {
		p.Skills, p.Agents = r.Status.Source.Path, r.Status.Agents.Source
	}
	return p
}

// read is the part of a Read that LoadPreview takes, from p.
func (p Paths) read() *Read {
	r := &Read{}
	if p.MCP != "" {
		r.MCP = &MCPJSON{SourcePath: p.MCP}
	}
	if p.Skills != "" || p.Agents != "" {
		r.Status = &StatusJSON{}
		r.Status.Source.Path, r.Status.Agents.Source = p.Skills, p.Agents
	}
	return r
}

// Reply is what a Call gives back: the field of its Op. Failed fills
// every one, so a caller reads its own field whatever went wrong.
type Reply struct {
	Result  Result // read
	Preview Preview
	Outcome Outcome // sync, update, toggle, uninstall, pull, bothways, upgrade
	Plan    Plan
	Audit   AuditResult
	Done    []string // install
	Err     string   // install's error, or why the call failed
	Machine Machine
	Project string // project: FindProject's answer
}

// Failed is the Reply of a call that could not run, saying why.
func Failed(why string) Reply {
	return Reply{Result: Result{Stderr: why, Code: -1}, Preview: Preview{Err: why}, Outcome: failed(why),
		Plan: Plan{Err: why}, Audit: AuditResult{Unknown: true}, Err: why, Machine: Machine{Err: why}}
}

// Do runs a Call: Serve on this machine, or a daemon's through the
// window's backend. step says what it does now; it may be called from
// another goroutine.
type Do func(ctx context.Context, c Call, step func(string)) Reply

// Serve does c with the client: what a daemon runs for a window. Secrets
// stay here: a read keeps only the fields the popup parses, and an MCP
// preview masks its values.
func (cl Client) Serve(ctx context.Context, c Call, step func(string)) Reply {
	if step == nil {
		step = func(string) {}
	}
	cwd := at(c.Scope, c.Dir)
	switch c.Op {
	case OpRead:
		args := Reads[c.Name]
		if args == nil {
			args = Checks[c.Name]
		}
		if args == nil || (c.Scope != Global && c.Scope != Project) {
			return Failed("unknown read " + c.Name)
		}
		return Reply{Result: trim(c.Name, cl.Run(ctx, cwd, append(slices.Clone(args), "--json", Flag(c.Scope))...))}
	case OpPreview:
		return Reply{Preview: cl.LoadPreview(c.Item, c.Paths.read())}
	case OpSync:
		return Reply{Outcome: cl.Sync(ctx, c.Scopes, c.Plugins, c.Dir, step)}
	case OpUpdate:
		return Reply{Outcome: cl.UpdateItems(ctx, c.Items, c.All, c.Dir, step)}
	case OpToggle:
		return Reply{Outcome: cl.Toggle(ctx, c.Item, c.Action, c.Dir, step)}
	case OpUninstall:
		return Reply{Outcome: cl.Uninstall(ctx, c.Item, c.Dir, step)}
	case OpDiscover:
		return Reply{Plan: cl.Discover(ctx, c.Source, c.Scope, cwd)}
	case OpAudit:
		return Reply{Audit: cl.Audit(ctx, c.Clone, c.Scope, cwd, c.Picks)}
	case OpInstall:
		done, errText := cl.Install(ctx, c.Source, c.Scope, cwd, c.Skills, c.Force)
		return Reply{Done: done, Err: errText}
	case OpDrop:
		Drop(c.Clone)
		return Reply{}
	case OpProject:
		return Reply{Project: FindProject(c.Dir)}
	case OpMachine:
		return Reply{Machine: cl.LoadMachine(ctx)}
	case OpPull:
		step("Pulling…")
		return Reply{Outcome: outcome(cl.Run(ctx, "", "pull"), "Pulled and synced", "skillshare pull")}
	case OpBothWays:
		step("Syncing both ways…")
		return Reply{Outcome: outcome(cl.Run(ctx, "", "push", "--pull", "-m", "Update skills from pitwall"), "Synced both ways: pushed, pulled and synced targets", "skillshare push --pull")}
	case OpUpgrade:
		step("Updating skillshare…")
		return Reply{Outcome: outcome(cl.Run(ctx, "", "upgrade", "--cli", "--force"), "Updated skillshare", "skillshare upgrade")}
	}
	return Failed("unknown skillshare operation " + c.Op)
}

func outcome(r Result, ok, what string) Outcome {
	if r.Code != 0 {
		return failure(r, what)
	}
	return done(ok)
}

// trim is res with its output cut to the fields Read keeps, so nothing
// else the CLI printed leaves the machine; a read that does not parse
// keeps only its error line.
func trim(name string, res Result) Result {
	var r Read
	if err := r.Parse(name, res); err != nil {
		return Result{Stderr: err.Error(), Code: max(res.Code, 1)}
	}
	v := map[string]any{"list": r.List, "diff": r.Diff, "mcp": r.MCP, "plugins": r.Plugins, "hooks": r.Hooks,
		"status": r.Status, "check": r.Check, "checkAgents": r.CheckAgents}[name]
	b, err := json.Marshal(v)
	if err != nil {
		return Result{Stderr: err.Error(), Code: 1}
	}
	return Result{Stdout: string(b)}
}

// Machine is one Machines card: the CLI's version and health, what the
// targets wait for, and the source's git state.
type Machine struct {
	Missing          bool   // skillshare is not on this machine's PATH
	Version, Latest  string // Latest is set when doctor says it is newer
	Errors, Warnings int    // doctor's
	Targets, Drift   int    // targets with skills on, resources a sync would send
	Git              Git
	Err              string
}

// Git is the source repository's state as of its last fetch.
type Git struct {
	Read, Repo            bool // Read: the state could be read
	Top, Branch, Upstream string
	Ahead, Behind, Dirty  int
}

// BothWays reports whether a plain pull would refuse: the source has
// work of its own and somewhere to push it.
func (g Git) BothWays() bool { return (g.Dirty > 0 || g.Ahead > 0) && g.Upstream != "" }

// Late reports whether the source needs a look: behind, or work of its own.
func (g Git) Late() bool { return g.Behind > 0 || g.Dirty > 0 || g.Ahead > 0 }

// Line is the card's Remote to Source line.
func (g Git) Line() string {
	switch {
	case !g.Read:
		return "could not be read"
	case !g.Repo:
		return "no git"
	case g.Upstream == "" && !g.Late():
		return "no remote"
	}
	var parts []string
	if g.Behind > 0 {
		parts = append(parts, "↓"+strconv.Itoa(g.Behind)+" behind")
	}
	if g.Dirty > 0 {
		parts = append(parts, strconv.Itoa(g.Dirty)+" uncommitted")
	}
	if g.Ahead > 0 {
		parts = append(parts, "↑"+strconv.Itoa(g.Ahead)+" to push")
	}
	if len(parts) == 0 {
		return "up to date"
	}
	return strings.Join(parts, " · ")
}

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/loader.js
// (loadMachine) and model.js (parseGitStatus).

// LoadMachine reads this machine's card. It fetches nothing: Behind is as
// of the source's last fetch.
func (cl Client) LoadMachine(ctx context.Context) Machine {
	if !cl.Installed() {
		return Machine{Missing: true}
	}
	var doctor, diff, status Result
	var wg sync.WaitGroup
	for _, j := range []struct {
		out  *Result
		args []string
	}{{&doctor, []string{"doctor", "--json", "-g"}}, {&diff, []string{"diff", "--json", "-g"}}, {&status, []string{"status", "--json", "-g"}}} {
		wg.Go(func() { *j.out = cl.Run(ctx, "", j.args...) })
	}
	wg.Wait()
	var m Machine
	var d struct {
		Summary struct{ Errors, Warnings int } `json:"summary"`
		Version struct {
			Current string `json:"current"`
			Latest  string `json:"latest"`
			Update  bool   `json:"update_available"`
		} `json:"version"`
	}
	if json.Unmarshal([]byte(doctor.Stdout), &d) == nil {
		m.Errors, m.Warnings, m.Version = d.Summary.Errors, d.Summary.Warnings, d.Version.Current
		if d.Version.Update {
			m.Latest = d.Version.Latest
		}
	}
	var st Read
	if err := st.Parse("status", status); err != nil {
		m.Err = err.Error()
		return m
	}
	if m.Version == "" {
		m.Version = st.Status.Version
	}
	for _, t := range st.Status.Targets {
		if t.SkillsEnabled == nil || *t.SkillsEnabled {
			m.Targets++
		}
	}
	var df Read
	if df.Parse("diff", diff) == nil {
		m.Drift = len(driftFrom(df.Diff))
	}
	if src := st.Status.Source.Path; src != "" && cl.Git != nil {
		m.Git = readGit(ctx, cl, src)
	}
	return m
}

func readGit(ctx context.Context, cl Client, dir string) Git {
	top, err := cl.Git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Git{Read: true}
	}
	top = strings.TrimSpace(top)
	out, err := cl.Git(ctx, top, "status", "--porcelain=v1", "--branch", "--untracked-files=all")
	if err != nil {
		return Git{}
	}
	return ParseGit(top, out)
}

// ParseGit reads `git status --porcelain=v1 --branch` of the repo at top.
func ParseGit(top, out string) Git {
	g := Git{Read: true, Repo: true, Top: top}
	for _, line := range strings.Split(out, "\n") {
		if head, ok := strings.CutPrefix(line, "## "); ok {
			head = strings.TrimPrefix(head, "No commits yet on ")
			head, track, _ := strings.Cut(head, " [")
			g.Branch, g.Upstream, _ = strings.Cut(head, "...")
			for _, part := range strings.Split(strings.TrimSuffix(track, "]"), ", ") {
				if n, ok := strings.CutPrefix(part, "ahead "); ok {
					g.Ahead, _ = strconv.Atoi(n)
				} else if n, ok := strings.CutPrefix(part, "behind "); ok {
					g.Behind, _ = strconv.Atoi(n)
				}
			}
		} else if len(line) > 3 {
			g.Dirty++
		}
	}
	return g
}
