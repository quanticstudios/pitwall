package remote

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Agent is an agent the phone's New form offers. ID is what POST /api/new
// names; the daemon alone knows what it runs.
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Program is the command that runs, a preset's agent, "" for a shell.
	Program string `json:"-"`
	Preset  string `json:"-"`
}

// Folder is a folder the New form offers; Git marks one that can take a
// worktree.
type Folder struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Git  bool   `json:"git,omitempty"`
}

// NewOptions is what the New form offers, GET /api/new's answer.
type NewOptions struct {
	Agents  []Agent  `json:"agents"`
	Folders []Folder `json:"folders"`
}

// NewTab is POST /api/new: a tab in Dir of the session named Session,
// running Agent, in a new worktree Branch when Worktree is set, with Brief
// as the agent's first turn. Preset and Cmd are what the handler resolved
// Agent to.
type NewTab struct {
	Session  string `json:"session"`
	Dir      string `json:"dir"`
	Agent    string `json:"agent"`
	Worktree bool   `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Brief    string `json:"brief,omitempty"`

	Preset string   `json:"-"`
	Cmd    []string `json:"-"`
}

// Terminal is the Agent ID of a plain shell.
const Terminal = "terminal"

// builtinAgents are the agent CLIs the form offers when they are on the
// daemon's PATH, by the names the desktop's New task dialog gives them.
var builtinAgents = []Agent{
	{ID: "claude", Name: "Claude Code", Program: "claude"},
	{ID: "codex", Name: "Codex", Program: "codex"},
	{ID: "agy", Name: "Antigravity", Program: "agy"},
}

// Agents are the form's agents: the built-in CLIs look finds, a plain
// terminal, then presets, as the New task dialog lists them.
func Agents(presets []config.AgentPreset, look func(string) error) []Agent {
	var out []Agent
	for _, a := range builtinAgents {
		if look(a.Program) == nil {
			out = append(out, a)
		}
	}
	out = append(out, Agent{ID: Terminal, Name: "Terminal"})
	for _, p := range presets {
		out = append(out, Agent{ID: "preset:" + p.Name, Name: p.Name, Program: p.Agent, Preset: p.Name})
	}
	return out
}

// Argv is what starts a with brief as its first turn: a preset's name with
// the arguments after its own, or a command; neither for a shell. brief
// goes in as one argument, never through a shell.
func (a Agent) Argv(brief string) (preset string, cmd []string) {
	if a.Program == "" {
		return "", nil
	}
	var prompt []string
	if brief != "" {
		prompt = config.PromptArgs(a.Program, brief)
	}
	if a.Preset != "" {
		return a.Preset, prompt
	}
	return "", append([]string{a.Program}, prompt...)
}

// Folders are the folders of session's tabs, its groups' folders, then the
// folders of its tabs closed since, in that order, each once and only
// while it is still a folder. A worktree pitwall made counts as its main
// checkout, which new worktrees come from. home shows as "~".
func Folders(st model.State, session, home string) []Folder {
	var out []Folder
	add := func(dir string, git bool) {
		if dir == "" {
			return
		}
		if i := slices.IndexFunc(out, func(f Folder) bool { return f.Path == dir }); i >= 0 {
			out[i].Git = out[i].Git || git
			return
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return
		}
		name := filepath.Base(dir)
		if dir == home {
			name = "~"
		}
		out = append(out, Folder{Path: dir, Name: name, Git: git})
	}
	i := slices.IndexFunc(st.Sessions, func(s model.Session) bool { return s.Name == session })
	if i >= 0 {
		id := st.Sessions[i].ID
		for _, w := range st.Ordered(id) {
			if !w.Detached && !w.Monitor {
				add(cmp.Or(w.WorktreeRoot, w.RepoRoot, w.Path), w.RepoRoot != "" || w.Branch != "")
			}
		}
		for _, p := range st.Projects {
			if p.SessionID == id {
				add(p.Root, p.Kind == model.ProjectGit)
			}
		}
	}
	for _, c := range slices.Backward(st.Closed) {
		if c.Session == session {
			add(cmp.Or(c.Tab.WorktreeRoot, c.Tab.RepoRoot, c.Tab.Path), c.Tab.RepoRoot != "")
		}
	}
	return out
}

// check cleans r and refuses what no tab can start from, for agent a:
// a folder that is not an absolute path to a folder ("~" is home), a
// worktree without a branch, a first message for a shell, or one a CLI
// would read as a flag.
func (r *NewTab) check(a Agent, home string) error {
	r.Dir, r.Branch, r.Brief = strings.TrimSpace(r.Dir), strings.TrimSpace(r.Branch), strings.TrimSpace(r.Brief)
	if rest, ok := strings.CutPrefix(r.Dir, "~"); ok && home != "" && (rest == "" || rest[0] == '/') {
		r.Dir = home + rest
	}
	if !r.Worktree {
		r.Branch = ""
	}
	switch {
	case !filepath.IsAbs(r.Dir):
		return errors.New("type the folder's full path")
	case r.Worktree && r.Branch == "":
		return errors.New("name the new worktree's branch")
	case a.Program == "" && r.Brief != "":
		return errors.New("a terminal takes no first message")
	case strings.HasPrefix(r.Brief, "-"):
		return errors.New("start the first message with something other than a dash")
	}
	r.Dir = filepath.Clean(r.Dir)
	if fi, err := os.Stat(r.Dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("there is no folder %s on this machine", r.Dir)
	}
	return nil
}

// newOptions serves GET /api/new: what the form offers for ?session=.
func (s *Server) newOptions(w http.ResponseWriter, r *http.Request, d Device) {
	writeJSON200(w, s.NewOptions(r.URL.Query().Get("session")))
}

// startTab serves POST /api/new: it opens the tab a NewTab describes and
// answers its id and its pane's.
func (s *Server) startTab(w http.ResponseWriter, r *http.Request, d Device) {
	var req NewTab
	if !decode(w, r, &req) {
		return
	}
	opts := s.NewOptions(req.Session)
	i := slices.IndexFunc(opts.Agents, func(a Agent) bool { return a.ID == req.Agent })
	if i < 0 {
		http.Error(w, "pick an agent this machine offers", http.StatusBadRequest)
		return
	}
	home, _ := os.UserHomeDir()
	if err := req.check(opts.Agents[i], home); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Preset, req.Cmd = opts.Agents[i].Argv(req.Brief)
	tab, pane, err := s.Start(req)
	if err != nil {
		log.Printf("remote: %q: new %s tab in %s refused: %q", d.Name, req.Agent, req.Dir, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q opened %s tab %s in %s (worktree %t)", d.Name, req.Agent, tab, req.Dir, req.Worktree) // never the brief
	writeJSON200(w, map[string]any{"tab": tab, "pane": pane})
}

// tabAction serves a POST that names one tab or pane, as "tab" or "pane",
// and runs do on it, logged as what.
func tabAction(key, what string, do func(string) error) func(http.ResponseWriter, *http.Request, Device) {
	return func(w http.ResponseWriter, r *http.Request, d Device) {
		var req map[string]string
		if !decode(w, r, &req) {
			return
		}
		id := req[key]
		if id == "" {
			http.Error(w, "name the "+key, http.StatusBadRequest)
			return
		}
		if err := do(id); err != nil {
			log.Printf("remote: %q: %s of %s %s refused: %q", d.Name, what, key, id, err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("remote: %q: %s of %s %s", d.Name, what, key, id)
		writeJSON200(w, map[string]any{})
	}
}
