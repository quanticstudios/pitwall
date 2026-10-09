package config

import "fmt"

// Git is [git]: what pitwall does with tabs' branches and pull requests.
type Git struct {
	MergeMethod    string `toml:"merge_method" enum:"squash,merge,rebase" doc:"How Merge PR merges a tab's pull request: gh pr merge --squash, --merge or --rebase"`
	ArchiveOnMerge *bool  `toml:"archive_on_merge" doc:"Archive a worktree tab pitwall made once its pull request merges: close its panes, remove the worktree and delete the branch, as Archive does. Off by default: Archive waits for a click"`
	ConflictRadar  *bool  `toml:"conflict_radar" doc:"Warn when tabs on different branches of one repository change the same files: an amber mark on both rows, red when merging their commits would conflict, the files in the hover card, and a notice the first time. Checked with the branch stats, every 30 seconds at most"`
}

// DefaultMergeMethod is [git] merge_method's default.
const DefaultMergeMethod = "squash"

func defaultGit() Git {
	off, on := false, true
	return Git{MergeMethod: DefaultMergeMethod, ArchiveOnMerge: &off, ConflictRadar: &on}
}

// resolveGit fills in s's [git] values from c.
func resolveGit(c Git, s *Settings) []issue {
	s.MergeMethod, s.ArchiveOnMerge = DefaultMergeMethod, c.ArchiveOnMerge != nil && *c.ArchiveOnMerge
	s.ConflictRadar = c.ConflictRadar == nil || *c.ConflictRadar
	switch c.MergeMethod {
	case "":
	case "squash", "merge", "rebase":
		s.MergeMethod = c.MergeMethod
	default:
		return []issue{{"git.merge_method", fmt.Sprintf("%q is not squash, merge or rebase; using squash", c.MergeMethod)}}
	}
	return nil
}
