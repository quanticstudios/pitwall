package daemon

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// The conflict radar scans radarDelay after the first stats refresh that
// asks for it, so a burst of refreshes makes one scan, and gives up on a
// scan that takes longer than radarBudget. Tests shorten both.
var (
	radarDelay  = 2 * time.Second
	radarBudget = 10 * time.Second
)

// radar is the conflict radar's state: which tabs' branches change the
// same files, in State.Overlaps.
type radar struct {
	mu      sync.Mutex
	ctx     context.Context // Serve's, which scans run under; nil before Serve
	pending bool            // a scan is due
	scan    sync.Mutex      // held through a scan, so scans never overlap

	// The fields below are guarded by scan.
	merges map[[2]string][]string // conflicting files by the pair of commits merged
	last   string                 // what the last finished scan read: its tabs and their stats

	// The fields below are guarded by Daemon.mu.
	told   map[string]bool // tab pair, file and conflict already in a notice
	notice string          // the State.Notice the radar last set
}

// kickRadar asks for a scan radarDelay from now, unless one is already
// due. Without the git reads or the setting it does nothing.
func (d *Daemon) kickRadar() {
	if d.o.Changes == nil || d.o.Conflicts == nil || d.o.Radar == nil {
		return
	}
	d.radar.mu.Lock()
	defer d.radar.mu.Unlock()
	if d.radar.pending {
		return
	}
	d.radar.pending = true
	time.AfterFunc(radarDelay, func() {
		d.radar.mu.Lock()
		d.radar.pending = false
		ctx := d.radar.ctx
		d.radar.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() == nil {
			d.scanRadar(ctx)
		}
	})
}

// radarTab is a tab on a branch, as a scan reads it.
type radarTab struct{ id, branch, dir string }

// scanRadar sets State.Overlaps from the tabs' branches, and the notice
// for pairs and files it has not told about. A scan cut short by
// radarBudget keeps the last result.
func (d *Daemon) scanRadar(ctx context.Context) {
	d.radar.scan.Lock()
	defer d.radar.scan.Unlock()
	on := d.o.Radar()
	d.mu.Lock()
	var tabs []radarTab
	branches := map[string]bool{}
	var sig strings.Builder
	fmt.Fprintln(&sig, on)
	for _, w := range d.st.Workspaces {
		if w.Branch != "" {
			t := radarTab{id: w.ID, branch: w.Branch, dir: d.st.LivePath(w)}
			tabs = append(tabs, t)
			branches[w.Branch] = true
			fmt.Fprintln(&sig, t, d.st.Stats[w.ID])
		}
	}
	d.mu.Unlock()
	// ponytail: a scan runs only when a tab, its branch or its stats moved,
	// so a new untracked file, which the stats leave out, waits for the next
	// change that shows; drop the check if that misses overlaps.
	if sig.String() == d.radar.last {
		return
	}

	var got map[string][]model.Overlap
	if on && len(branches) > 1 {
		ctx, cancel := context.WithTimeout(ctx, radarBudget)
		defer cancel()
		var err error
		if got, err = d.overlaps(ctx, tabs); err != nil {
			log.Printf("conflict radar: %q", err)
			return
		}
	}
	d.radar.last = sig.String()

	d.mu.Lock()
	defer d.mu.Unlock()
	changed := len(got) > 0 || len(d.st.Overlaps) > 0
	if changed && reflect.DeepEqual(got, d.st.Overlaps) {
		changed = false
	}
	d.st.Overlaps = got
	if d.radarNotice() || changed {
		d.changed()
	}
}

// overlaps is, per tab, the other tabs in the same repository on another
// branch in another worktree whose changes share files with its own, and
// which of those files a merge of the two branches' commits would leave in
// conflict. nil when there are none.
// ponytail: reads the changes of every tab on a branch once two branches
// exist anywhere, single-branch repositories included; read each
// repository's common dir first if that gets slow.
func (d *Daemon) overlaps(ctx context.Context, tabs []radarTab) (map[string][]model.Overlap, error) {
	type side struct {
		radarTab
		c     *gitstat.Changes
		files map[string]bool
	}
	read := map[string]*gitstat.Changes{} // by dir: tabs in one folder read it once
	var sides []side
	for _, t := range tabs {
		c, ok := read[t.dir]
		if !ok {
			got, err := d.o.Changes(ctx, t.dir)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err == nil {
				c = &got
			}
			read[t.dir] = c
		}
		if c == nil {
			continue
		}
		files := map[string]bool{}
		for _, f := range append(slices.Clone(c.Committed), c.Uncommitted...) {
			files[f] = true
		}
		sides = append(sides, side{t, c, files})
	}

	merges := map[[2]string][]string{}
	out := map[string][]model.Overlap{}
	for i, a := range sides {
		for _, b := range sides[i+1:] {
			if a.c.Common != b.c.Common || a.branch == b.branch {
				continue
			}
			var both []string
			for f := range a.files {
				if b.files[f] {
					both = append(both, f)
				}
			}
			if len(both) == 0 {
				continue
			}
			slices.Sort(both)
			var clash []string
			if a.c.Head != b.c.Head && slices.ContainsFunc(a.c.Committed, func(f string) bool { return slices.Contains(b.c.Committed, f) }) {
				key := [2]string{min(a.c.Head, b.c.Head), max(a.c.Head, b.c.Head)}
				files, ok := d.radar.merges[key]
				if !ok {
					var err error
					files, err = d.o.Conflicts(ctx, a.c.Root, a.c.Head, b.c.Head)
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					if err != nil {
						log.Printf("conflict radar: %q", err)
						files = nil // asked again next scan
					} else {
						merges[key] = files
					}
				} else {
					merges[key] = files
				}
				clash = slices.DeleteFunc(slices.Clone(both), func(f string) bool { return !slices.Contains(files, f) })
			}
			files := append(clash, slices.DeleteFunc(both, func(f string) bool { return slices.Contains(clash, f) })...)
			out[a.id] = append(out[a.id], model.Overlap{WorkspaceID: b.id, Branch: b.branch, Files: files, Conflicts: len(clash)})
			out[b.id] = append(out[b.id], model.Overlap{WorkspaceID: a.id, Branch: a.branch, Files: files, Conflicts: len(clash)})
		}
	}
	d.radar.merges = merges // only the pairs still about
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// radarNotice puts, in State.Notice, the pairs of tabs that started to
// share files or to conflict since the last notice, and reports whether it
// did. Another notice on screen holds it back until it is dismissed.
// Callers hold d.mu.
func (d *Daemon) radarNotice() bool {
	if d.st.Notice != "" && d.st.Notice != d.radar.notice {
		return false
	}
	if d.radar.told == nil {
		d.radar.told = map[string]bool{}
	}
	var says, keys []string
	for _, id := range slices.Sorted(maps.Keys(d.st.Overlaps)) {
		for _, o := range d.st.Overlaps[id] {
			if o.WorkspaceID < id {
				continue // the pair once, from its first tab
			}
			var fresh []string
			conflict := false
			for i, f := range o.Files {
				k := id + "\x00" + o.WorkspaceID + "\x00" + f
				if i < o.Conflicts {
					k += "\x00conflict"
				}
				if !d.radar.told[k] {
					fresh, keys = append(fresh, f), append(keys, k)
					conflict = conflict || i < o.Conflicts
				}
			}
			if len(fresh) > 0 {
				says = append(says, radarSentence(d.tabName(id, ""), d.tabName(o.WorkspaceID, o.Branch), fresh, conflict))
			}
		}
	}
	if len(says) == 0 {
		return false
	}
	for _, k := range keys {
		d.radar.told[k] = true
	}
	d.radar.notice = strings.Join(says, " ")
	d.st.Notice = d.radar.notice
	return true
}

// radarSentence is the notice for tabs a and b starting to share files,
// conflicting ones first.
func radarSentence(a, b string, files []string, conflict bool) string {
	what := files[0]
	switch n := len(files) - 1; {
	case n == 1:
		what += " and 1 more file"
	case n > 1:
		what += fmt.Sprintf(" and %d more files", n)
	}
	if conflict {
		return fmt.Sprintf("%s and %s would conflict in %s.", a, b, what)
	}
	return fmt.Sprintf("%s and %s both edit %s.", a, b, what)
}

// tabName is what the sidebar calls tab id, else its branch, else fallback.
// Callers hold d.mu.
func (d *Daemon) tabName(id, fallback string) string {
	w := d.workspace(id)
	if w == nil {
		return fallback
	}
	name := w.Label
	if w.NameSet || name == "" {
		name = w.Name
	}
	if name == "" {
		name = w.Branch
	}
	return cmp.Or(name, fallback)
}

// radarSetting reads [git] conflict_radar from the config at path, again
// only when the file changed.
func radarSetting(path string) func() bool {
	return setting(path, true, func(s config.Settings) bool { return s.ConflictRadar })
}
