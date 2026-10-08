package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Worktrees is [worktrees], and the whole of a repo's .pitwall/worktree.toml.
type Worktrees struct {
	PortBase *float64 `toml:"port_base" min:"1024" max:"65535" doc:"Each worktree tab pitwall makes gets the lowest free block of port_step ports at port_base + N × port_step, N from 1 (3010-3019 first, by default), so the main checkout keeps port_base. Its panes get PORT, PITWALL_PORT_BASE and PITWALL_PORTS"`
	PortStep *float64 `toml:"port_step" min:"0" max:"1000" doc:"How many ports each worktree tab gets; 0 gives them none"`
	Copy     []string `toml:"copy" doc:"Files copied from the main checkout into a new worktree that lacks them, such as [\".env\", \".env.local\"]. Paths are relative to the repo root. Empty by default: .env files hold secrets"`
	Link     []string `toml:"link" doc:"Paths linked (symlinked) from the main checkout into a new worktree that lacks them, such as [\"node_modules\"]. A shared node_modules breaks when branches need different dependencies"`
	Setup    string   `toml:"setup" doc:"A command typed into the shell of a new worktree tab, such as \"pnpm install\", after copy and link. From config.toml it runs in that pane, in view; from a repo's .pitwall/worktree.toml it waits there for you to press Enter"`
}

// WorktreeSettings is [worktrees] with every value filled in.
type WorktreeSettings struct {
	PortBase, PortStep int
	Copy, Link         []string
	Setup              string
	// SetupFromRepo is true when Setup came from the repo's WorktreeFile,
	// which anyone who wrote the repo controls: it is typed in for the user
	// to read and run, never run on its own.
	SetupFromRepo bool
}

// Port defaults.
const (
	DefaultPortBase = 3000
	DefaultPortStep = 10
)

func defaultWorktrees() Worktrees {
	base, step := float64(DefaultPortBase), float64(DefaultPortStep)
	return Worktrees{PortBase: &base, PortStep: &step, Copy: []string{}, Link: []string{}}
}

// resolveWorktrees is c over base: each value c sets replaces base's,
// except a bad one, which is reported. prefix starts the reported paths.
func resolveWorktrees(c Worktrees, base WorktreeSettings, prefix string) (WorktreeSettings, []issue) {
	var issues []issue
	num := func(p *float64, name string, lo, hi float64, into *int) {
		switch {
		case p == nil:
		case *p < lo || *p > hi || *p != float64(int(*p)):
			issues = append(issues, issue{prefix + name, fmt.Sprintf("%g is not a whole number from %g to %g", *p, lo, hi)})
		default:
			*into = int(*p)
		}
	}
	num(c.PortBase, "port_base", 1024, 65535, &base.PortBase)
	num(c.PortStep, "port_step", 0, 1000, &base.PortStep)
	paths := func(ps []string, name string, into *[]string) {
		if ps == nil {
			return
		}
		*into = nil
		for _, p := range ps {
			// invariant: copy and link never reach outside the repo.
			if !filepath.IsLocal(p) {
				issues = append(issues, issue{prefix + name, fmt.Sprintf("%q is not a path inside the repo", p)})
				continue
			}
			*into = append(*into, filepath.Clean(p))
		}
	}
	paths(c.Copy, "copy", &base.Copy)
	paths(c.Link, "link", &base.Link)
	if c.Setup != "" {
		base.Setup = c.Setup
	}
	return base, issues
}

// WorktreeFile is a repo's own worktree settings, relative to its root.
const WorktreeFile = ".pitwall/worktree.toml"

// LoadWorktrees is [worktrees] from the config at path with repo's
// WorktreeFile over it, for the daemon. The problems are the repo file's;
// config.toml's are the GUI's and `config check`'s to report.
func LoadWorktrees(path, repo string) (WorktreeSettings, []Problem) {
	s, _ := LoadFile(path)
	data, err := os.ReadFile(filepath.Join(repo, WorktreeFile))
	if errors.Is(err, os.ErrNotExist) {
		return s.Worktrees, nil
	}
	if err != nil {
		return s.Worktrees, []Problem{{File: WorktreeFile, Msg: err.Error()}}
	}
	var c Worktrees
	probs := parse(WorktreeFile, data, &c, nil)
	w, issues := resolveWorktrees(c, s.Worktrees, "")
	w.SetupFromRepo = c.Setup != ""
	return w, append(probs, locate(WorktreeFile, data, issues)...)
}
