package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestApprovalWhy: the card's muted line keeps a real reason and drops a
// detail that restates the tool and what it runs, as Claude sends them.
func TestApprovalWhy(t *testing.T) {
	str := func(s string) *string { return &s }
	edit := &session.ToolRequest{ToolName: "Edit", FilePath: "/tmp/acme-api/main.go"}
	for _, c := range []struct {
		a    session.Approval
		want string
	}{
		{session.Approval{Detail: str("Edit: main.go"), Request: edit}, ""},
		{session.Approval{Detail: str(`Edit: {"file_path":"/tmp/acme-api/main.go"}`), Request: edit}, ""},
		{session.Approval{Detail: str("Edit needs approval"), Request: edit}, ""},
		{session.Approval{Detail: str("/tmp/acme-api/main.go"), Request: edit}, ""},
		{session.Approval{Detail: str("Bash is not in this session's allow list."), Request: &session.ToolRequest{ToolName: "Bash", Command: "git push"}},
			"Bash is not in this session's allow list."},
		{session.Approval{Detail: str("Run the tests")}, ""},
	} {
		if got := approvalWhy(c.a); got != c.want {
			t.Errorf("approvalWhy(%q) = %q, want %q", *c.a.Detail, got, c.want)
		}
	}
}
