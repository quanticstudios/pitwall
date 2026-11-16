package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// MainRoot is the main worktree of the repo at dir, also from inside a
// linked worktree.
func MainRoot(ctx context.Context, dir string) (string, error) {
	trees, err := gitstat.ListWorktrees(ctx, dir)
	if err != nil || len(trees) == 0 {
		return "", errors.New("not in a git repository")
	}
	return trees[0].Path, nil
}

// WorktreeFrom turns worktree new's --from, --branch and --pr into what
// the worktree starts from. A branch with no local branch of that name
// may name a remote one.
func WorktreeFrom(ctx context.Context, root, from, branch string, pr int) (model.WorktreeFrom, error) {
	set := 0
	for _, on := range []bool{from != "", branch != "", pr != 0} {
		if on {
			set++
		}
	}
	if set > 1 {
		return model.WorktreeFrom{}, errors.New("use one of --from, --branch and --pr")
	}
	switch {
	case pr != 0:
		return model.WorktreeFrom{Kind: model.FromPR, PR: pr}, nil
	case branch != "":
		refs, err := gitstat.ListRefs(ctx, root)
		if err != nil {
			return model.WorktreeFrom{}, err
		}
		if slices.Contains(refs.Local, branch) {
			return model.WorktreeFrom{Kind: model.FromBranch, Ref: branch}, nil
		}
		if slices.Contains(refs.Remote, branch) {
			return model.WorktreeFrom{Kind: model.FromRemote, Ref: branch}, nil
		}
		return model.WorktreeFrom{}, fmt.Errorf("no local or remote branch %q", branch)
	}
	return model.WorktreeFrom{Ref: from}, nil
}

// Lane turns worktree new --lane into the lane's preset, its brief (the
// words of brief joined, with the worker contract before them) and the
// [[hosts]] name its workers start on, "" for this machine.
func Lane(s config.Settings, lane, preset string, brief []string) (string, []string, string, error) {
	if preset != "" {
		return "", nil, "", errors.New("use one of --preset and --lane")
	}
	if !s.Fleet.On {
		return "", nil, "", errors.New("--lane needs fleet mode: turn it on in Settings, Fleet, or set enabled = true under [fleet] in config.toml")
	}
	l, ok := s.Fleet.Lane(lane)
	if !ok {
		return "", nil, "", fmt.Errorf("no lane %q with a preset; see pitwall worktree lanes", lane)
	}
	if len(brief) == 0 {
		return "", nil, "", errors.New(`--lane needs a brief: pitwall worktree new <name> --lane small -- "<brief>"`)
	}
	text, err := s.Fleet.Brief(strings.Join(brief, " "))
	if err != nil {
		return "", nil, "", err
	}
	return l.Preset, []string{text}, l.Host, nil
}

// Orphans are the tabs of session on this machine whose parent tab is
// gone and that no other machine started: the ones adopt moves here
// without a host.
func Orphans(st model.State, session string) []model.Workspace {
	var out []model.Workspace
	for _, w := range st.Workspaces {
		if w.SessionID == session && !w.RemoteParent && w.Parent != "" && st.Workspace(w.Parent) == nil {
			out = append(out, w)
		}
	}
	return out
}

// Queued is session's queued tasks, first to start first, as pitwall
// queue ls numbers them.
func Queued(st model.State, session string) []model.Task {
	return slices.DeleteFunc(slices.Clone(st.Tasks), func(t model.Task) bool { return t.SessionID != session })
}

// QueuedTask is the task of tasks that n, "3" or "#3" as pitwall queue ls
// numbers them, names.
func QueuedTask(tasks []model.Task, n string) (model.Task, error) {
	i, err := strconv.Atoi(strings.TrimPrefix(n, "#"))
	if err != nil || i < 1 || i > len(tasks) {
		return model.Task{}, fmt.Errorf("no queued task %s (see pitwall queue ls)", n)
	}
	return tasks[i-1], nil
}
