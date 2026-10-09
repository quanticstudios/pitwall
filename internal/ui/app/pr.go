package app

import (
	"fmt"
	"image/color"
	"log"
	"strconv"
	"strings"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// mergeScript merges pull request $2 of the repository at $1 by method
// $3 (squash, merge or rebase), printing the command first.
const mergeScript = `cd -- "$1" || exit
echo "\$ gh pr merge $2 --$3"
exec gh pr merge "$2" "--$3"
`

// mergeCmd is the argv of a tab merging pull request number at dir.
func mergeCmd(dir string, number int, method string) []string {
	return []string{"sh", "-c", mergeScript, "sh", dir, strconv.Itoa(number), method}
}

// rerunScript re-runs the failed jobs of the latest failed workflow run on
// branch $2 of the repository at $1, printing each command first.
const rerunScript = `cd -- "$1" || exit
echo "\$ gh run list --branch $2 --status failure --limit 1"
id=$(gh run list --branch "$2" --status failure --limit 1 --json databaseId --jq '.[0].databaseId') || exit
if [ -z "$id" ]; then
	echo 'No failed run on this branch.'
	exit 1
fi
echo "\$ gh run rerun $id --failed"
exec gh run rerun "$id" --failed
`

// rerunCmd is the argv of a tab re-running the failed checks of branch at dir.
func rerunCmd(dir, branch string) []string {
	return []string{"sh", "-c", rerunScript, "sh", dir, branch}
}

// prActs are the [keys] actions on the open tab's pull request.
var prActs = map[string]bool{"open_pr": true, "merge_pr": true, "rerun_checks": true}

// prBlocked is why the palette's open_pr, merge_pr or rerun_checks cannot
// run on the open tab; ok is false for any other action.
func (n *nav) prBlocked(st *model.State, act string) (why string, ok bool) {
	if !prActs[act] {
		return "", false
	}
	ws := findWorkspace(st, n.workspace)
	if ws == nil {
		return "no tab", true
	}
	open, merge, rerun := sidebar.PRBlocked(st, *ws, ghInstalled())
	switch act {
	case "open_pr":
		return open, true
	case "merge_pr":
		return merge, true
	}
	return rerun, true
}

// prAction runs act, "open_pr", "merge_pr" or "rerun_checks", on tab id's
// pull request: the browser on its URL, the merge dialog, or gh run rerun
// in a tab of its own in id's group, as create_pr opens one. A blocked act
// does nothing.
func (u *ui) prAction(st *model.State, id, act string) {
	ws := findWorkspace(st, id)
	if ws == nil {
		return
	}
	open, merge, rerun := sidebar.PRBlocked(st, *ws, ghInstalled())
	pr := st.PRs[id]
	switch {
	case act == "open_pr" && open == "":
		openLink(pr.URL)
	case act == "merge_pr" && merge == "":
		u.modal.open(modalMerge, id)
	case act == "rerun_checks" && rerun == "":
		dir := st.LivePath(*ws)
		u.nav.expectSession(st)
		u.send(proto.NewSession{Name: fmt.Sprintf("Checks #%d", pr.Number), Cwd: dir, GroupID: ws.ProjectID, SessionID: ws.SessionID, Loose: ws.ProjectID == "", Cmd: rerunCmd(dir, ws.Branch)})
	default:
		log.Printf("%s on tab %s: %q %q %q", act, id, open, merge, rerun)
	}
}

// confirmMerge is the merge dialog's Merge button. With failed checks the
// first click only arms it, and the second merges.
func (u *ui) confirmMerge() {
	m := &u.modal
	st := u.b.State()
	ws := findWorkspace(&st, m.ws)
	if ws == nil {
		m.close()
		return
	}
	if _, merge, _ := sidebar.PRBlocked(&st, *ws, ghInstalled()); merge != "" {
		m.close() // merged, closed or gone meanwhile
		return
	}
	pr := st.PRs[m.ws]
	if pr.CI() == model.CheckFail && !m.armed {
		m.armed = true
		return
	}
	dir := st.LivePath(*ws)
	u.nav.expectSession(&st)
	u.send(proto.NewSession{Name: fmt.Sprintf("Merge #%d", pr.Number), Cwd: dir, GroupID: ws.ProjectID, SessionID: ws.SessionID, Loose: ws.ProjectID == "", Cmd: mergeCmd(dir, pr.Number, u.mergeMethod())})
	m.close()
}

// mergeMethod is [git] merge_method, squash when unset.
func (u *ui) mergeMethod() string {
	if u.cfg.MergeMethod == "" {
		return "squash"
	}
	return u.cfg.MergeMethod
}

// mergeBody is the merge dialog: what runs, what the checks say, and a
// Merge button that asks again when checks failed.
func (u *ui) mergeBody(gtx gl.Context, st *model.State, ws *model.Workspace) gl.Dimensions {
	th, m := u.th, &u.modal
	pr := st.PRs[ws.ID]
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, fmt.Sprintf("Merge #%d?", pr.Number))
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 14, th.Muted, "Merges "+ws.Branch+" on GitHub. The command runs in a new tab:")
		}),
		gl.Rigid(gl.Spacer{Height: 4}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.MonoFont, 12, th.Fg, fmt.Sprintf("gh pr merge %d --%s", pr.Number, u.mergeMethod()))
		}),
	}
	warn := func(col color.NRGBA, s string) {
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, 13, col, s)
		}))
	}
	switch failed := pr.Failed(); pr.CI() {
	case model.CheckFail:
		warn(th.Red, fmt.Sprintf("%s failed: %s.", plural(len(failed), "check"), strings.Join(failed, ", ")))
	case model.CheckPending:
		warn(th.Yellow, "Checks are still running.")
	}
	if pr.Review == model.ReviewChanges {
		warn(th.Yellow, "A reviewer requested changes.")
	}
	if pr.Conflicts {
		warn(th.Yellow, "GitHub reports merge conflicts.")
	}
	ok, kind := "Merge", kit.Primary
	if m.armed {
		warn(th.Fg, "Merge with failing checks? Click again to merge.")
		ok, kind = "Merge anyway", kit.Danger
	}
	kids = append(kids,
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return u.buttons(gtx, "Cancel", ok, kind) }),
	)
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}
