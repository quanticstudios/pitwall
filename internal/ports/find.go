package ports

import (
	"cmp"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Root is a process a tab's servers come from: a pane's leader, or a
// chat's agent process.
type Root struct {
	Tab string // the workspace id
	PID int
	// Session: the leader of the pane's session (a pane starts its command
	// with setsid), whose session holds what the pane started even after
	// a parent exits and the process is reparented.
	Session bool
	// Agent: an agent runs under it, so its servers read Agent, not
	// Terminal.
	Agent bool
}

// Find lists, by tab, the processes under roots that listen on a TCP
// port. A process under a chat's agent belongs to that chat's tab first,
// then one under a pane's leader, then one in a pane's session. loose are
// more roots whose processes belong to no tab until place names one, as
// the Codex app-server that several panes share: Find never walks into
// them from a pane. skip names processes never listed (the agents
// themselves, the caller); listening gets the candidates' ports.
func Find(t Table, roots []Root, loose []int, place func(pid int) string, skip func(Proc) bool, listening func([]int) map[int][]int) map[string][]model.Server {
	kids := t.Children()
	isLoose := map[int]bool{}
	for _, l := range loose {
		isLoose[l] = true
	}
	owner := map[int]Root{}
	claim := func(r Root, pid int) {
		if _, ok := owner[pid]; !ok {
			owner[pid] = r
		}
	}
	// Chat agents first: their processes are theirs even inside a pane's session.
	slices.SortStableFunc(roots, func(a, b Root) int {
		if a.Session == b.Session {
			return 0
		}
		if a.Session {
			return 1
		}
		return -1
	})
	for _, r := range roots {
		if _, ok := t[r.PID]; !ok {
			continue
		}
		for _, pid := range under(kids, r.PID, isLoose) {
			claim(r, pid)
		}
	}
	sessions := map[int]Root{}
	for _, r := range roots {
		if r.Session {
			if _, ok := t[r.PID]; ok {
				sessions[r.PID] = r
			}
		}
	}
	for pid, p := range t {
		if r, ok := sessions[p.SID]; ok && !isLoose[pid] {
			claim(r, pid)
		}
	}
	looseOf := map[int]bool{}
	for _, l := range loose {
		if _, ok := t[l]; !ok {
			continue
		}
		for _, pid := range Under(kids, l) {
			if _, ok := owner[pid]; !ok {
				looseOf[pid] = true
			}
		}
	}
	var cands []int
	for pid := range owner {
		if !skip(t[pid]) {
			cands = append(cands, pid)
		}
	}
	for pid := range looseOf {
		if !skip(t[pid]) {
			cands = append(cands, pid)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	slices.Sort(cands)
	out := map[string][]model.Server{}
	for pid, ps := range listening(cands) {
		if len(ps) == 0 {
			continue
		}
		r, ok := owner[pid]
		if !ok && looseOf[pid] && place != nil {
			if tab := place(pid); tab != "" {
				r, ok = Root{Tab: tab, Agent: true}, true
			}
		}
		if !ok {
			continue
		}
		out[r.Tab] = append(out[r.Tab], model.Server{PID: pid, Name: t[pid].Name, Ports: slices.Clone(ps), Agent: r.Agent})
	}
	for _, ss := range out {
		slices.SortFunc(ss, func(a, b model.Server) int {
			return cmp.Or(cmp.Compare(a.Ports[0], b.Ports[0]), cmp.Compare(a.PID, b.PID))
		})
	}
	return out
}

// under is Under that does not walk into stop's processes.
func under(kids map[int][]int, root int, stop map[int]bool) []int {
	if stop[root] {
		return nil
	}
	seen := map[int]bool{root: true}
	out := []int{root}
	for i := 0; i < len(out) && len(out) < walkLimit; i++ {
		for _, c := range kids[out[i]] {
			if !seen[c] && !stop[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// Group is the process group to signal to stop pid: its own group when
// none of keep's processes is in it (a shell with job control and an
// agent's tool shell give each job a group of its own, so the group is the
// job: npm and the node it started go together), else 0, and only pid is
// signalled.
func Group(t Table, pid int, keep map[int]bool) int {
	g := t[pid].PGID
	if g <= 1 {
		return 0
	}
	for q, p := range t {
		if p.PGID == g && keep[q] {
			return 0
		}
	}
	return g
}
