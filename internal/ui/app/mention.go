package app

import (
	"context"
	"path"
	"slices"
	"strings"
	"time"
)

// mentionFresh is how long a folder's listing serves the composer's @
// mentions before the next query lists it again.
const mentionFresh = 30 * time.Second

// maxMentionRows bounds the matches an @ query ranks.
const maxMentionRows = 50

// mentionList is a chat folder's files and folders for @ mentions,
// listed off the UI goroutine, and the matches for the last query.
type mentionList struct {
	paths []string // nil until the first listing is in
	done  chan pathList
	at    time.Time // when the last listing started
	query string
	rows  []string
}

// mentions is chatin.Env.Mentions for a chat in dir: git's tracked and
// untracked, not ignored files there, as the path picker lists them, and
// their folders. Nil for a chat on another machine or with no folder.
func (u *ui) mentions(dir string) func(string) []string {
	if dir == "" || curHost() != "" {
		return nil // ponytail: a host's files would need a proto request
	}
	return func(q string) []string {
		if u.mentionLists == nil {
			u.mentionLists = map[string]*mentionList{}
		}
		l := u.mentionLists[dir]
		if l == nil {
			l = &mentionList{}
			u.mentionLists[dir] = l
		}
		if l.done != nil {
			select {
			case r := <-l.done:
				l.done, l.paths, l.query = nil, withFolders(r.files), "\x00"
			default:
			}
		}
		if l.done == nil && time.Since(l.at) > mentionFresh {
			done := make(chan pathList, 1)
			l.done, l.at = done, time.Now()
			invalidate := u.invalidate
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				files, err := listPaths(ctx, dir)
				done <- pathList{files, err}
				if invalidate != nil {
					invalidate()
				}
			}()
		}
		if l.paths == nil {
			return nil
		}
		if q != l.query {
			l.query, l.rows = q, rankMentions(l.paths, q)
		}
		return l.rows
	}
}

// withFolders is files, sorted, with each folder they are in as a path
// ending in a slash.
func withFolders(files []string) []string {
	out := slices.Clone(files)
	seen := map[string]bool{}
	for _, f := range files {
		for d := path.Dir(f); d != "." && d != "/" && !seen[d]; d = path.Dir(d) {
			seen[d] = true
			out = append(out, d+"/")
		}
	}
	slices.Sort(out)
	return out
}

// rankMentions is the paths an @ query names, best first: with no query
// the top folder's entries, after a folder's slash that folder's own,
// else the path picker's fuzzy match on the whole path and its name, a
// name equal to the query first.
func rankMentions(paths []string, q string) []string {
	if q == "" || strings.HasSuffix(q, "/") {
		var rows []string
		for _, p := range paths {
			rest, ok := strings.CutPrefix(p, q)
			if ok && rest != "" && !strings.Contains(strings.TrimSuffix(rest, "/"), "/") {
				rows = append(rows, p)
			}
		}
		// Folders first, as a file tree lists them.
		slices.SortStableFunc(rows, func(a, b string) int {
			return boolInt(!strings.HasSuffix(a, "/")) - boolInt(!strings.HasSuffix(b, "/"))
		})
		return rows[:min(len(rows), maxMentionRows)]
	}
	rows, _ := rankPaths(paths, q, maxMentionRows)
	// A name the query spells out whole goes first: @app is app/ before approve/.
	exact := func(p string) int {
		return boolInt(!strings.EqualFold(path.Base(strings.TrimSuffix(p, "/")), q))
	}
	slices.SortStableFunc(rows, func(a, b string) int { return exact(a) - exact(b) })
	return rows
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
