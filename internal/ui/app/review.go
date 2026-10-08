package app

import (
	"log"
	"os/exec"
	"sync"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// ghInstalled reports whether the gh CLI is on PATH; tests replace it.
// ponytail: looked up once, so gh installed while the window is open counts
// after a restart.
var ghInstalled = sync.OnceValue(func() bool {
	_, err := exec.LookPath("gh")
	return err == nil
})

// pagerScript runs the git command in "$@" at the top of the repository
// holding $1, into the user's pager: $GIT_PAGER, else core.pager, else delta
// when installed, else less -R. LESS gains -R and loses -F, so a short diff
// stays until q closes the pane, unless the pager command sets -F itself.
const pagerScript = `cd -- "$1" && cd -- "$(git rev-parse --show-toplevel)" || exit
shift
p=${GIT_PAGER:-$(git config core.pager)}
if [ -z "$p" ]; then
	if command -v delta >/dev/null 2>&1; then p=delta; else p='less -R'; fi
fi
export LESS="$LESS -R -+F" DELTA_PAGER="${DELTA_PAGER:-less -R}"
"$@" | eval "$p"
`

// diffCmd is the argv of a pane showing, in the user's pager, the changes
// of the repository at dir from its merge base with base, the default
// branch's ref, to the work tree: of file only when it is not nil, as the
// side panel's Changes lists it. An untracked file shows whole.
func diffCmd(dir, base string, file *gitstat.FileStat) []string {
	cmd := []string{"sh", "-c", pagerScript, "sh", dir, "git", "-c", "color.ui=always", "diff"}
	switch {
	case file == nil:
		return append(cmd, "--merge-base", base)
	case file.Status == '?':
		return append(cmd, "--no-index", "--", "/dev/null", file.Path)
	}
	return append(cmd, "--merge-base", base, "--", file.Path)
}

// prScript pushes the branch at $1 when it has no upstream, never with
// force, then runs gh pr create --fill, printing each command first.
const prScript = `cd -- "$1" || exit
if ! git rev-parse --verify --quiet '@{upstream}' >/dev/null 2>&1; then
	echo '$ git push -u origin HEAD'
	git push -u origin HEAD || exit
fi
echo '$ gh pr create --fill'
exec gh pr create --fill
`

// prCmd is the argv of a tab opening a pull request for the branch at dir.
func prCmd(dir string) []string { return []string{"sh", "-c", prScript, "sh", dir} }

// review runs act, "view_diff" or "create_pr", for tab id: the diff in a
// pane beside the tab's panes, which q closes, or gh in a tab of its own
// in id's group, which stays when gh exits with its output on screen, and
// which a daemon restart never runs again. It returns nil when
// sidebar.ReviewBlocked says the tab cannot.
func (n *nav) review(st *model.State, id, act string) any {
	ws := findWorkspace(st, id)
	if ws == nil {
		return nil
	}
	diff, pr := sidebar.ReviewBlocked(st, *ws, ghInstalled())
	dir := st.LivePath(*ws) // where the branch stats come from
	switch {
	case act == "view_diff" && diff == "":
		if id == n.workspace {
			n.expectPane(st)
		}
		return proto.OpenPane{WorkspaceID: id, Dir: layout.Horizontal, Cmd: diffCmd(dir, st.Stats[id].Base, nil)}
	case act == "create_pr" && pr == "":
		n.expectSession(st)
		return proto.NewSession{Name: "PR " + ws.Branch, Cwd: dir, GroupID: ws.ProjectID, SessionID: ws.SessionID, Loose: ws.ProjectID == "", Cmd: prCmd(dir)}
	}
	log.Printf("%s on tab %s: %q %q", act, id, diff, pr)
	return nil
}

// reviewBlocked is why the palette's view_diff or create_pr cannot run on
// the open tab, "" when it can or for any other action.
func (n *nav) reviewBlocked(st *model.State, act string) string {
	if act != "view_diff" && act != "create_pr" {
		return ""
	}
	ws := findWorkspace(st, n.workspace)
	if ws == nil {
		return "no tab"
	}
	diff, pr := sidebar.ReviewBlocked(st, *ws, ghInstalled())
	if act == "view_diff" {
		return diff
	}
	return pr
}
