package app

import (
	"log"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// reviewerPaths are the reviewers' CLIs, found once as New task finds the
// agents, "" when missing. On a Host they are the bare names, which the
// daemon looks up there.
var reviewerPaths = sync.OnceValue(func() map[model.Provider]string {
	out := map[model.Provider]string{}
	for _, r := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		out[r] = string(r)
		if Host != "" {
			continue
		}
		out[r] = ""
		if found, err := exec.LookPath(string(r)); err == nil {
			// why: the daemon runs it with its own PATH, as pitwall new does.
			out[r], _ = filepath.Abs(found)
		}
	}
	return out
})

// reviewersInstalled is which reviewers' CLIs reviewerPaths found.
func reviewersInstalled() map[model.Provider]bool {
	out := map[model.Provider]bool{}
	for r, path := range reviewerPaths() {
		out[r] = path != ""
	}
	return out
}

// opinionBlocked is why tab ws cannot get a second opinion from reviewer,
// "" for the tab's default reviewer, "" when it can.
func opinionBlocked(st *model.State, ws *model.Workspace, reviewer model.Provider) string {
	if ws == nil {
		return "no tab"
	}
	if _, git := st.Stats[ws.ID]; !git {
		return "no git"
	}
	if reviewer == "" {
		reviewer = sidebar.Reviewers(st, *ws)[0]
	}
	return sidebar.OpinionBlocked(st, *ws, reviewerPaths()[reviewer] != "")
}

// secondOpinion asks reviewer, "" for the default one, to review tab id's
// changes beside its agent. A blocked one does nothing.
func (u *ui) secondOpinion(st *model.State, id string, reviewer model.Provider) {
	ws := findWorkspace(st, id)
	if why := opinionBlocked(st, ws, reviewer); why != "" {
		log.Printf("second opinion on tab %s: %q", id, why)
		return
	}
	if reviewer == "" {
		reviewer = sidebar.Reviewers(st, *ws)[0]
	}
	if !u.send(proto.SecondOpinion{Pane: sidebar.Reviewed(st, *ws).ID, Agent: reviewerPaths()[reviewer]}) {
		u.sendFailed("Second opinion: the pitwall daemon running is older than this window. It works once the daemon restarts.")
	}
}
