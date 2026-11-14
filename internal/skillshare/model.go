// Package skillshare reads the skillshare CLI (github.com/runkids/skillshare)
// for pitwall's skillshare popup: what its source holds, what waits for a
// sync or has an update, and the commands that fix it. It has no Gio
// imports; internal/ui/app draws it.
//
// The parsing and the state rules follow the skillshare panel for Herdr.
// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/model.js
package skillshare

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Kind is a resource kind, as the Library's tabs name them.
type Kind string

const (
	Skills  Kind = "skills"
	Agents  Kind = "agents"
	MCP     Kind = "mcp"
	Plugins Kind = "plugins"
	Hooks   Kind = "hooks"
)

// Kinds are the kinds in tab order.
var Kinds = []Kind{Skills, Agents, MCP, Plugins, Hooks}

// Label is the kind's tab title.
func (k Kind) Label() string {
	switch k {
	case MCP:
		return "MCP"
	case "":
		return ""
	}
	return strings.ToUpper(string(k[:1])) + string(k[1:])
}

// State is what a row needs.
type State string

const (
	InSync  State = "ok"
	Changed State = "sync"    // a target holds an older or different copy
	New     State = "missing" // no target has it yet
	Update  State = "update"  // its source has a newer version
	Off     State = "off"     // disabled
	Unknown State = "unknown" // a read it depends on is pending or failed
)

// Scopes the CLI reads: the global source and a project's.
const (
	Global  = "global"
	Project = "project"
)

// Item is one row: a skill, an agent, an MCP server, a plugin or a hook,
// or a hook notice (Notice) the plan reported without a hook's name.
type Item struct {
	Key, Scope string
	Kind       Kind
	Name       string // the CLI's name: a skill's flat name, an agent without .md
	Label      string // the last part of Rel
	Rel        string // the path in the source, an agent's without .md
	Path       string // the list's relPath, as enable and disable take it
	Src        string // where it came from, for the row's second line
	Version    string
	Disabled   bool
	State      State
	Reason     string
	Targets    []string // targets it waits on (skills, agents) or goes to
	Repo       string   // the tracked repository it came from
	Update     string   // the name `update` takes

	Notice   bool     // a hook notice
	Pending  bool     // a notice with native changes that Sync applies
	Lines    []string // a notice's or hook's plan lines
	Declared bool     // a hook defined in the source, not only in the plan
	Root     string   // a hook's project root, "" for the top level
}

// Read is one scope's answers. A nil field has not answered or failed;
// Pending and Errors say which.
type Read struct {
	List        []ListEntry
	Diff        *DiffJSON
	MCP         *MCPJSON
	Plugins     *PluginsJSON
	Hooks       *HooksJSON
	Status      *StatusJSON
	Check       *CheckJSON
	CheckAgents []CheckEntry

	Pending map[string]bool   // reads running now
	Errors  map[string]string // reads that failed, by name
}

// Reads are the commands every refresh runs per scope, by the name Read
// keeps them under. check and check agents ask each source's remote, so
// they run less often (Checks).
var Reads = map[string][]string{
	"list":    {"list", "--all"},
	"diff":    {"diff"},
	"mcp":     {"mcp", "list"},
	"plugins": {"plugin", "list"},
	"hooks":   {"hooks", "list"},
	"status":  {"status"},
}

// Checks are the update checks, by name.
var Checks = map[string][]string{
	"check":       {"check"},
	"checkAgents": {"check", "agents"},
}

// ReadNames are Reads' names in a fixed order.
var ReadNames = []string{"list", "diff", "mcp", "plugins", "hooks", "status"}

// The CLI's JSON, only the fields the popup reads.
type (
	ListEntry struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		RelPath  string `json:"relPath"`
		Source   string `json:"source"`
		Disabled bool   `json:"disabled"`
		RepoName string `json:"repoName"`
	}
	DiffItem struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
		IsSync bool   `json:"is_sync"`
	}
	DiffJSON struct {
		Targets []struct {
			Name  string     `json:"name"`
			Error string     `json:"error"`
			Items []DiffItem `json:"items"`
		} `json:"targets"`
	}
	MCPJSON struct {
		SourcePath string `json:"sourcePath"`
		Changes    []struct {
			Target string `json:"target"`
			Name   string `json:"name"`
			Action string `json:"action"`
		} `json:"changes"`
	}
	PluginsJSON struct {
		Packages map[string]struct {
			Source   string `json:"source"`
			Version  string `json:"version"`
			Bindings map[string]struct {
				ID      string `json:"id"`
				Version string `json:"version"`
				Pending bool   `json:"pending"`
				Sync    *bool  `json:"sync"`
			} `json:"bindings"`
		} `json:"packages"`
	}
	hookSource struct {
		Entries map[string]struct {
			Description string         `json:"description"`
			Enabled     *bool          `json:"enabled"`
			Bindings    map[string]any `json:"bindings"`
		} `json:"entries"`
	}
	HookChange struct {
		Target         string `json:"target"`
		Name           string `json:"name"`
		Root           string `json:"root"`
		Action         string `json:"action"`
		Message        string `json:"message"`
		InactiveReason string `json:"inactiveReason"`
	}
	HooksJSON struct {
		Source *struct {
			hookSource
			Projects map[string]hookSource `json:"projects"`
		} `json:"source"`
		Plan *struct {
			Changes  []HookChange `json:"changes"`
			Warnings []string     `json:"warnings"`
		} `json:"plan"`
		Error        string `json:"error"`
		PreviewError string `json:"previewError"`
	}
	StatusJSON struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Targets []struct {
			Name          string `json:"name"`
			SkillsEnabled *bool  `json:"skills_enabled"`
		} `json:"targets"`
		Agents struct {
			Source  string `json:"source"`
			Targets []struct {
				Name string `json:"name"`
			} `json:"targets"`
		} `json:"agents"`
		Version string `json:"version"`
	}
	CheckEntry struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Version string `json:"version"`
		Status  string `json:"status"`
	}
	CheckJSON struct {
		TrackedRepos []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Behind int    `json:"behind"`
		} `json:"tracked_repos"`
		Skills []CheckEntry `json:"skills"`
	}
)

// Parse stores read name's stdout in r: nil when it took, else why not.
// A failed command's message is its JSON error or its last output line.
func (r *Read) Parse(name string, res Result) error {
	out := []byte(res.Stdout)
	var err error
	switch name {
	case "list":
		var v []ListEntry
		if err = json.Unmarshal(out, &v); err == nil {
			r.List = v
		}
	case "diff":
		err = parseInto(out, &r.Diff)
		if err == nil {
			for _, t := range r.Diff.Targets {
				if t.Error != "" {
					err = fmt.Errorf("%s: %s", t.Name, t.Error)
					break
				}
			}
		}
	case "mcp":
		err = parseInto(out, &r.MCP)
	case "plugins":
		err = parseInto(out, &r.Plugins)
	case "hooks":
		err = parseInto(out, &r.Hooks)
	case "status":
		err = parseInto(out, &r.Status)
	case "check":
		err = parseInto(out, &r.Check)
	case "checkAgents":
		var v []CheckEntry
		if err = json.Unmarshal(out, &v); err == nil {
			r.CheckAgents = v
		}
	default:
		return fmt.Errorf("unknown read %q", name)
	}
	if res.Code == 0 && err != nil && name == "diff" && r.Diff != nil {
		return err
	}
	if res.Code != 0 || err != nil {
		args := Reads[name]
		if args == nil {
			args = Checks[name]
		}
		return fmt.Errorf("%s", Failure(res, "skillshare "+strings.Join(args, " ")))
	}
	return nil
}

func parseInto[T any](b []byte, dst **T) error {
	v := new(T)
	if err := json.Unmarshal(b, v); err != nil {
		return err
	}
	*dst = v
	return nil
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// LastLine is the last non-empty line of text, without color codes.
func LastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(ansi.ReplaceAllString(text, "")), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return strings.TrimLeft(l, "✗ ")
		}
	}
	return ""
}

// Failure is what went wrong with res: its JSON error, else the last line
// of stderr or stdout, else its exit code.
func Failure(res Result, what string) string {
	var j struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(res.Stdout), &j) == nil && j.Error != "" {
		return LastLine(j.Error)
	}
	return cmp.Or(LastLine(res.Stderr), LastLine(res.Stdout), fmt.Sprintf("%s exited with %d", what, res.Code))
}

// Flag is the CLI flag for a scope.
func Flag(scope string) string {
	if scope == Project {
		return "-p"
	}
	return "-g"
}

var urlPrefix = regexp.MustCompile(`^https?://`)
var dotGit = regexp.MustCompile(`\.git(/|$)`)

func shortSource(s string) string {
	s = dotGit.ReplaceAllString(urlPrefix.ReplaceAllString(s, ""), "$1")
	return strings.TrimSuffix(s, "/")
}

// waiting is a skill's or agent's drift from diff: what a sync would do
// and on which targets.
type waiting struct {
	action, reason string
	targets        []string
}

func driftFrom(d *DiffJSON) map[string]*waiting {
	out := map[string]*waiting{}
	if d == nil {
		return out
	}
	for _, t := range d.Targets {
		for _, it := range t.Items {
			if !it.IsSync {
				continue
			}
			kind := Skills
			if it.Kind == "agent" {
				kind = Agents
			}
			key := string(kind) + ":" + strings.TrimSuffix(it.Name, ".md")
			w := out[key]
			if w == nil {
				w = &waiting{action: it.Action, reason: it.Reason}
				out[key] = w
			} else if w.action != "modify" {
				w.action = it.Action
			}
			w.targets = append(w.targets, t.Name)
		}
	}
	return out
}

// BuildItems is one scope's rows of every kind.
func BuildItems(scope string, r *Read) []Item {
	var items []Item
	drift := driftFrom(r.Diff)
	checks := map[string]CheckEntry{}
	behind := map[string]int{}
	if r.Check != nil {
		for _, s := range r.Check.Skills {
			checks["skills:"+s.Name] = s
		}
		for _, t := range r.Check.TrackedRepos {
			if t.Status == "behind" {
				behind[t.Name] = t.Behind
			}
		}
	}
	for _, s := range r.CheckAgents {
		checks["agents:"+s.Name] = s
	}

	for _, s := range r.List {
		kind := Skills
		if s.Kind == "agent" {
			kind = Agents
		}
		flat := s.Name
		if kind == Agents {
			flat = strings.TrimSuffix(s.Name, ".md")
		}
		rel := strings.TrimSuffix(cmp.Or(s.RelPath, s.Name), ".md")
		ch, ok := checks[string(kind)+":"+rel]
		if !ok {
			ch = checks[string(kind)+":"+flat]
		}
		it := Item{
			Key: scope + ":" + string(kind) + ":" + flat, Kind: kind, Scope: scope, Name: flat,
			Label: rel[strings.LastIndex(rel, "/")+1:], Rel: rel, Path: cmp.Or(s.RelPath, s.Name),
			Disabled: s.Disabled, Version: ch.Version, State: InSync, Repo: s.RepoName,
		}
		it.Src = shortSource(cmp.Or(ch.Source, s.Source))
		switch {
		case it.Src != "":
		case s.RepoName != "":
			it.Src = "tracked repo " + s.RepoName
		case strings.Contains(rel, "/"):
			it.Src = rel
		default:
			it.Src = "local"
		}
		n, isBehind := behind[s.RepoName]
		switch {
		case it.Disabled:
			it.State = Off
		case ch.Status == "update_available" || s.RepoName != "" && isBehind:
			it.State, it.Update = Update, rel
			if isBehind {
				it.Update = s.RepoName
				it.Reason = fmt.Sprintf("%d behind", n)
			}
		default:
			if d := drift[string(kind)+":"+flat]; d != nil {
				it.State, it.Reason, it.Targets = Changed, d.reason, d.targets
				if d.action == "add" {
					it.State = New
				}
			}
		}
		items = append(items, it)
	}

	if r.MCP != nil {
		type server struct{ targets, pending []string }
		by := map[string]*server{}
		var order []string
		for _, c := range r.MCP.Changes {
			s := by[c.Name]
			if s == nil {
				s = &server{}
				by[c.Name] = s
				order = append(order, c.Name)
			}
			s.targets = append(s.targets, c.Target)
			if c.Action != "" && c.Action != "unchanged" {
				s.pending = append(s.pending, c.Target+": "+c.Action)
			}
		}
		for _, name := range order {
			s := by[name]
			it := Item{Key: scope + ":mcp:" + name, Kind: MCP, Scope: scope, Name: name, Label: name, Src: "MCP server",
				State: InSync, Reason: strings.Join(s.pending, ", "), Targets: s.targets}
			if len(s.pending) > 0 {
				it.State = Changed
			}
			items = append(items, it)
		}
	}

	if r.Plugins != nil {
		for _, name := range sortedKeys(r.Plugins.Packages) {
			p := r.Plugins.Packages[name]
			var pending, targets []string
			first := ""
			for _, t := range sortedKeys(p.Bindings) {
				b := p.Bindings[t]
				targets = append(targets, t)
				first = cmp.Or(first, b.ID)
				if b.Pending || b.Sync != nil && !*b.Sync {
					pending = append(pending, t)
				}
			}
			it := Item{Key: scope + ":plugins:" + name, Kind: Plugins, Scope: scope, Name: name, Label: name,
				Src: cmp.Or(shortSource(cmp.Or(p.Source, first)), "plugin"), Version: p.Version, Targets: targets, State: InSync}
			switch {
			case len(pending) > 0:
				it.State, it.Reason = Changed, "pending in "+strings.Join(pending, ", ")
			case len(targets) == 0:
				it.State = New
			}
			items = append(items, it)
		}
	}

	items = append(items, hookItems(scope, r)...)

	// A row whose reads are pending or failed is unknown, not in sync.
	for i := range items {
		it := &items[i]
		keys := []string{string(it.Kind)}
		if it.Kind == Skills || it.Kind == Agents {
			keys = []string{"list", "diff", "status"}
		}
		if it.Notice || it.State == Off {
			continue
		}
		for _, k := range keys {
			if r.Pending[k] || r.Errors[k] != "" {
				it.State = Unknown
			}
		}
	}
	return items
}

func hookItems(scope string, r *Read) []Item {
	h := r.Hooks
	if h == nil {
		return nil
	}
	by := map[string]*Item{}
	var order []string
	add := func(key string, it Item) *Item {
		if by[key] == nil {
			it.Key, it.Kind, it.Scope = key, Hooks, scope
			by[key] = &it
			order = append(order, key)
		}
		return by[key]
	}
	hook := func(root, name string) *Item {
		key := scope + ":hooks:" + name
		if root != "" {
			key = scope + ":hooks:" + root + "/" + name
		}
		return add(key, Item{Root: root, Name: name, Label: name})
	}
	notice := func(id, root string) *Item {
		return add(scope+":hooks:status:"+id, Item{Root: root, Name: "Hook status", Label: "Hook status", Notice: true})
	}
	if h.Source != nil {
		sources := []struct {
			root string
			src  hookSource
		}{{"", h.Source.hookSource}}
		for _, root := range sortedKeys(h.Source.Projects) {
			sources = append(sources, struct {
				root string
				src  hookSource
			}{root, h.Source.Projects[root]})
		}
		for _, s := range sources {
			for _, name := range sortedKeys(s.src.Entries) {
				e := s.src.Entries[name]
				it := hook(s.root, name)
				it.Declared, it.Disabled, it.Reason = true, e.Enabled != nil && !*e.Enabled, e.Description
				it.Targets = sortedKeys(e.Bindings)
			}
		}
	}
	var changes []HookChange
	var warnings []string
	if h.Plan != nil {
		changes, warnings = h.Plan.Changes, h.Plan.Warnings
	}
	acts := map[string][]HookChange{}
	for i, c := range changes {
		var it *Item
		if c.Name != "" {
			it = hook(c.Root, c.Name)
		} else {
			it = notice(fmt.Sprint(i), c.Root)
		}
		if c.Target != "" && !slices.Contains(it.Targets, c.Target) {
			it.Targets = append(it.Targets, c.Target)
		}
		acts[it.Key] = append(acts[it.Key], c)
	}
	errText := cmp.Or(h.Error, h.PreviewError)
	if errText != "" {
		notice("error", "").Lines = []string{errText}
	}
	for i, w := range warnings {
		notice(fmt.Sprintf("warning:%d", i), "").Lines = []string{w}
	}
	line := func(c HookChange) string {
		var parts []string
		if head := strings.Join(nonEmpty(c.Target, c.Action), ": "); head != "" {
			parts = append(parts, head)
		}
		for _, s := range nonEmpty(c.Message, c.InactiveReason) {
			if !slices.Contains(parts, s) {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " · ")
	}
	var out []Item
	for _, key := range order {
		it := by[key]
		pending := false
		for _, c := range acts[key] {
			if c.Action != "unchanged" && c.Action != "inactive" {
				pending = true
			}
			if errText == "" {
				it.Lines = append(it.Lines, line(c))
			}
			if it.Notice && slices.Contains([]string{"add", "update", "remove", "adopt", "release"}, c.Action) {
				it.Pending = true
			}
		}
		it.Src = cmp.Or(strings.Join(it.Targets, ", "), "no agents")
		if it.Notice {
			it.Reason = strings.Join(it.Lines, "; ")
		} else if pending {
			var ls []string
			for _, c := range acts[key] {
				if c.Action != "unchanged" {
					ls = append(ls, line(c))
				}
			}
			it.Reason = strings.Join(ls, "; ")
		}
		switch {
		case h.Plan == nil:
			it.State = Unknown
		case pending:
			it.State = Changed
		case it.Disabled:
			it.State = Off
		default:
			it.State = InSync
		}
		out = append(out, *it)
	}
	return out
}

func nonEmpty(s ...string) []string {
	return slices.DeleteFunc(s, func(x string) bool { return x == "" })
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// WaitsForSync reports whether Sync would act on it: a row that is new or
// changed, or a hook notice with native changes.
func (it Item) WaitsForSync() bool {
	if it.Notice {
		return it.Pending
	}
	return it.State == Changed || it.State == New
}

// Shown reports whether scope s shows under the switch's scope ("all",
// Project or Global).
func Shown(scope, s string) bool { return scope == "all" || scope == s }

// Target is one target and how many resources wait on it.
type Target struct {
	Name string
	N    int
}

// Targets is every target of the shown scopes, most waiting first, and
// whether a diff answered. diff counts skills and agents; each waiting MCP
// server, plugin or hook adds one to the targets it waits on. A target
// whose diff is pending or failed counts nothing.
func Targets(reads map[string]*Read, scope string, items []Item) (known bool, list []Target) {
	by := map[string]int{}
	for _, s := range sortedKeys(reads) {
		r := reads[s]
		if !Shown(scope, s) {
			continue
		}
		unsure := r.Pending["diff"] || r.Errors["diff"] != ""
		if r.Diff != nil {
			known = true
			for _, t := range r.Diff.Targets {
				n := 0
				for _, it := range t.Items {
					if it.IsSync && !unsure {
						n++
					}
				}
				by[t.Name] += n
			}
		}
		if r.Status != nil {
			for _, t := range r.Status.Targets {
				by[t.Name] += 0
			}
		}
	}
	for _, it := range items {
		if !Shown(scope, it.Scope) || it.Notice || !it.WaitsForSync() || it.Kind == Skills || it.Kind == Agents {
			continue
		}
		for _, t := range waitingOn(it) {
			if _, ok := by[t]; ok {
				by[t]++
			}
		}
	}
	for name, n := range by {
		list = append(list, Target{name, n})
	}
	slices.SortFunc(list, func(a, b Target) int { return cmp.Or(b.N-a.N, strings.Compare(a.Name, b.Name)) })
	return known, list
}

// waitingOn is the targets an MCP server, plugin or hook waits on.
func waitingOn(it Item) []string {
	var out []string
	switch it.Kind {
	case MCP:
		for _, p := range strings.Split(it.Reason, ", ") {
			if t, _, ok := strings.Cut(p, ": "); ok {
				out = append(out, t)
			}
		}
	case Plugins:
		for _, t := range strings.Split(strings.TrimPrefix(it.Reason, "pending in "), ", ") {
			out = append(out, t)
		}
	case Hooks:
		out = it.Targets
	}
	return out
}

// Answer is Overview's headline: Kind one of "loading", "failed",
// "moving", "waiting" or "ok".
type Answer struct{ Kind, Headline, Sub string }

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func andMore(ts []Target) string {
	switch len(ts) {
	case 0:
		return ""
	case 1:
		return ts[0].Name
	case 2:
		return ts[0].Name + " and " + ts[1].Name
	}
	return fmt.Sprintf("%s, %s and %d more", ts[0].Name, ts[1].Name, len(ts)-2)
}

// Overview is the headline for what the shown scopes need: syncing while
// a sync runs, reading while a read is pending, unconfirmed when one
// failed, else how many targets wait, else in sync. pending is the rows
// Sync would act on, lag the targets with something waiting.
func Overview(moving bool, reading []string, failed bool, pending int, lag []Target, total, targets int) Answer {
	switch {
	case moving:
		if len(lag) > 0 {
			return Answer{"moving", "Syncing " + plural(len(lag), "target"), andMore(lag)}
		}
		return Answer{"moving", "Syncing", "every target"}
	case len(reading) > 0:
		return Answer{"loading", "Reading skillshare", "Still reading " + strings.Join(reading, ", ")}
	case failed:
		return Answer{"failed", "Some sync states are unconfirmed", "A read failed; what it could read is below"}
	case pending > 0 || len(lag) > 0:
		head := plural(pending, "resource") + " to sync"
		if len(lag) > 0 {
			verb := " need a sync"
			if len(lag) == 1 {
				verb = " needs a sync"
			}
			head = plural(len(lag), "target") + verb
		}
		sub := "waiting on " + andMore(lag)
		if pending > 0 {
			sub = plural(pending, "resource") + " waiting"
			if len(lag) > 0 {
				sub += " for " + andMore(lag)
			}
		}
		return Answer{"waiting", head, sub}
	}
	return Answer{"ok", "Everything is in sync", plural(total, "resource") + " · " + plural(targets, "target")}
}

// Unsure are the kinds whose count is not known yet in the shown scopes:
// a read they need is pending or failed.
func Unsure(reads map[string]*Read, scope string) map[Kind]bool {
	out := map[Kind]bool{}
	for s, r := range reads {
		if !Shown(scope, s) {
			continue
		}
		for _, k := range []struct {
			read  string
			kinds []Kind
		}{{"list", []Kind{Skills, Agents}}, {"mcp", []Kind{MCP}}, {"plugins", []Kind{Plugins}}, {"hooks", []Kind{Hooks}}} {
			if r.Pending[k.read] || r.Errors[k.read] != "" {
				for _, kd := range k.kinds {
					out[kd] = true
				}
			}
		}
	}
	return out
}

// Counts is how many rows each kind has in the shown scopes, notices left
// out.
func Counts(items []Item, scope string) map[Kind]int {
	out := map[Kind]int{}
	for _, it := range items {
		if !it.Notice && Shown(scope, it.Scope) {
			out[it.Kind]++
		}
	}
	return out
}

// Groups are a kind's rows in the Library's sections.
type Groups struct {
	Unknown, Look, Sync, Update, InSync, Off []Item
}

// Group sorts kind's rows of the shown scopes into the Library's
// sections, each by label.
func Group(items []Item, scope string, kind Kind) Groups {
	var g Groups
	for _, it := range items {
		if !Shown(scope, it.Scope) || it.Kind != kind {
			continue
		}
		switch {
		case it.Notice:
			g.Look = append(g.Look, it)
		case it.State == Unknown:
			g.Unknown = append(g.Unknown, it)
		case it.State == Off:
			g.Off = append(g.Off, it)
		case it.State == Update:
			g.Update = append(g.Update, it)
		case it.WaitsForSync():
			g.Sync = append(g.Sync, it)
		default:
			g.InSync = append(g.InSync, it)
		}
	}
	for _, l := range []*[]Item{&g.Unknown, &g.Look, &g.Sync, &g.Update, &g.InSync, &g.Off} {
		slices.SortStableFunc(*l, func(a, b Item) int { return strings.Compare(a.Label, b.Label) })
	}
	return g
}

// SyncScopes are the scopes Sync runs in for the shown scope: each with a
// waiting row or a waiting target, else every shown one.
func SyncScopes(items []Item, reads map[string]*Read, scope string) []string {
	var out []string
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, it := range items {
		if Shown(scope, it.Scope) && it.WaitsForSync() {
			add(it.Scope)
		}
	}
	for s, r := range reads {
		if !Shown(scope, s) || r.Diff == nil {
			continue
		}
		for _, t := range r.Diff.Targets {
			if slices.ContainsFunc(t.Items, func(i DiffItem) bool { return i.IsSync }) {
				add(s)
			}
		}
	}
	if len(out) == 0 {
		for s := range reads {
			if Shown(scope, s) {
				add(s)
			}
		}
	}
	slices.Sort(out)
	return out
}

// RowState is a row's one short state; the preview has the rest.
func RowState(it Item, showScope bool) string {
	pre := ""
	if showScope {
		pre = it.Scope + " · "
	}
	if it.Notice {
		return it.Scope
	}
	switch it.State {
	case Update:
		return pre + "update available"
	case New:
		return pre + "new"
	case Changed:
		return pre + "changed"
	case Unknown:
		return pre + "sync status unknown"
	case Off:
		return pre + "disabled"
	}
	if it.Kind == MCP || it.Kind == Hooks {
		word := "target"
		if it.Kind == Hooks {
			word = "agent"
		}
		return pre + plural(len(it.Targets), word)
	}
	if it.Kind == Plugins && it.Version != "" {
		return pre + it.Src + " · " + it.Version
	}
	return pre + it.Src
}

// ToggleArgs are the arguments that enable or disable a skill or an agent
// (action "enable" or "disable"), nil when the row cannot take it. The
// pattern is the entry's own path, as the CLI's dashboard writes it.
func ToggleArgs(it Item, action string) []string {
	if it.Kind == Hooks {
		if !it.Declared || it.Root != "" || it.Notice || action != "enable" && action != "disable" || (action == "enable") != it.Disabled {
			return nil
		}
		return []string{"hooks", action, it.Name, Flag(it.Scope)}
	}
	if (it.Kind != Skills && it.Kind != Agents) || it.Path == "" {
		return nil
	}
	want := "disable"
	if it.Disabled {
		want = "enable"
	}
	if action != want {
		return nil
	}
	args := []string{action, it.Path}
	if it.Kind == Agents {
		args = append(args, "--kind", "agent")
	}
	return append(args, Flag(it.Scope))
}

// UpdateArgs update it, or with all every skill or agent of its scope.
func UpdateArgs(it Item, all bool) []string {
	args := []string{"update"}
	if it.Kind == Agents {
		args = append(args, "agents")
	}
	if all {
		args = append(args, "--all")
	} else {
		args = append(args, it.Update)
	}
	return append(args, "--json", Flag(it.Scope))
}

// UninstallArgs move it to the CLI's trash. A project uninstall needs
// --force, or it asks.
func UninstallArgs(it Item) []string {
	if it.Kind == Hooks {
		if !it.Declared || it.Root != "" || it.Notice {
			return nil
		}
		return []string{"hooks", "remove", it.Name, Flag(it.Scope)}
	}
	args := []string{"uninstall"}
	if it.Kind == Agents {
		args = append(args, "agents")
	}
	args = append(args, it.Rel, "--json", Flag(it.Scope))
	if it.Scope == Project {
		args = append(args, "--force")
	}
	return args
}

// AuditPrompt is what Ask the agent sends about the skill or agent at
// path.
func AuditPrompt(path string) string { return "audit this skill: " + path }
