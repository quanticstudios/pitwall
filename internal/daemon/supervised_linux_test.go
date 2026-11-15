package daemon

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestSupervisedUnit(t *testing.T) {
	const user = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/"
	for _, c := range []struct {
		cgroup, parent, unit string
		user                 bool
	}{
		{cgroup: user + "pitwall.service\n", parent: "systemd", unit: "pitwall.service", user: true},
		{cgroup: "0::/system.slice/pitwall.service\n", parent: "systemd", unit: "pitwall.service"},
		// cgroup v2 next to v1 controllers: only the unified line counts.
		{cgroup: "12:pids:/system.slice/x.service\n0::/system.slice/pitwall.service\n", parent: "systemd", unit: "pitwall.service"},
		{cgroup: "0::/user.slice/user-1000.slice/session-3.scope\n", parent: "systemd"},
		{cgroup: user + "app-foot-1.scope\n", parent: "systemd"},
		// A unit's process started the daemon, which the unit does not run.
		{cgroup: user + "app-foot@1.service\n", parent: "foot"},
		{cgroup: "12:pids:/system.slice/x.service\n", parent: "systemd"},
		{cgroup: "", parent: "systemd"},
	} {
		unit, isUser := supervisedUnit(c.cgroup, c.parent)
		if unit != c.unit || isUser != c.user {
			t.Errorf("%q under %s: unit %q, user %v; want %q, %v", c.cgroup, c.parent, unit, isUser, c.unit, c.user)
		}
	}
}

// A daemon that systemd runs as a service refuses a handoff and keeps
// serving, since the unit would stop the panes it leaves behind.
func TestSupervisedDaemonRefusesHandoff(t *testing.T) {
	defer func(f func() (string, bool)) { supervised = f }(supervised)
	supervised = func() (string, bool) { return "pitwall.service", true }
	f := &fakes{statsCalls: map[string]int{}}
	_, sock, stop := runDaemon(t, f, func(*Options) {})
	defer stop()
	_, _, err := Takeover(sock, f.options())
	if err == nil || !strings.Contains(err.Error(), "runs as the systemd unit pitwall.service") || !strings.Contains(err.Error(), "systemctl --user restart pitwall.service") {
		t.Fatalf("Takeover: %v, want a refusal naming the unit", err)
	}
	dial(t, sock, "gui").waitState("the old daemon's state", func(model.State) bool { return true })
}
