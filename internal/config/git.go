package config

import "fmt"

// Git is [git]: what pitwall does with a tab's pull request.
type Git struct {
	MergeMethod    string `toml:"merge_method" enum:"squash,merge,rebase" doc:"How Merge PR merges a tab's pull request: gh pr merge --squash, --merge or --rebase"`
	ArchiveOnMerge *bool  `toml:"archive_on_merge" doc:"Archive a worktree tab pitwall made once its pull request merges: close its panes, remove the worktree and delete the branch, as Archive does. Off by default: Archive waits for a click"`
}

// DefaultMergeMethod is [git] merge_method's default.
const DefaultMergeMethod = "squash"

func defaultGit() Git {
	off := false
	return Git{MergeMethod: DefaultMergeMethod, ArchiveOnMerge: &off}
}

// resolveGit fills in s's [git] values from c.
func resolveGit(c Git, s *Settings) []issue {
	s.MergeMethod, s.ArchiveOnMerge = DefaultMergeMethod, c.ArchiveOnMerge != nil && *c.ArchiveOnMerge
	switch c.MergeMethod {
	case "":
	case "squash", "merge", "rebase":
		s.MergeMethod = c.MergeMethod
	default:
		return []issue{{"git.merge_method", fmt.Sprintf("%q is not squash, merge or rebase; using squash", c.MergeMethod)}}
	}
	return nil
}
