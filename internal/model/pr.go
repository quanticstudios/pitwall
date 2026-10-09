package model

// PRState is a pull request's state on its forge.
type PRState string

const (
	PROpen   PRState = "open"
	PRMerged PRState = "merged"
	PRClosed PRState = "closed"
)

// CheckState is one CI check's state, or the rollup of all of them.
type CheckState string

const (
	CheckNone    CheckState = "" // no checks
	CheckPending CheckState = "pending"
	CheckPass    CheckState = "pass"
	CheckFail    CheckState = "fail"
	CheckSkip    CheckState = "skip" // skipped, neutral or cancelled: counts for nothing
)

// Review is a pull request's review decision.
type Review string

const (
	ReviewNone     Review = ""
	ReviewRequired Review = "required"
	ReviewApproved Review = "approved"
	ReviewChanges  Review = "changes" // changes requested
)

// PR is the pull request of a tab's branch, as gh reports it.
type PR struct {
	Number int
	State  PRState
	Draft  bool
	URL    string
	Review Review
	// Conflicts is true when GitHub says the branch cannot merge cleanly.
	Conflicts bool
	Checks    []Check
}

// Check is one CI check of a PR: a check run or a commit status.
type Check struct {
	Name  string
	State CheckState
}

// CI rolls the checks up: fail when any failed, else pending when any
// runs, else pass when any passed, else CheckNone.
func (p PR) CI() CheckState {
	out := CheckNone
	for _, c := range p.Checks {
		switch {
		case c.State == CheckFail:
			return CheckFail
		case c.State == CheckPending:
			out = CheckPending
		case c.State == CheckPass && out == CheckNone:
			out = CheckPass
		}
	}
	return out
}

// Failed is the names of the checks that failed.
func (p PR) Failed() []string {
	var out []string
	for _, c := range p.Checks {
		if c.State == CheckFail {
			out = append(out, c.Name)
		}
	}
	return out
}
