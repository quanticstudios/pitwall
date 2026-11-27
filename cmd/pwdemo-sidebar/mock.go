package main

import (
	"os"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// mockState is the sidebar mock's data for -mock: "tiles" for its column
// of four machines, "ladder" for one tab per state on this machine. It sets
// sb's hosts and returns the state and the active tab.
func mockState(sb *sidebar.Sidebar, kind string, now time.Time) (model.State, string) {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	home, _ := os.UserHomeDir()
	st := model.State{Stats: map[string]model.BranchStats{}}
	on := map[string]string{}
	add := func(host, id, title, branch, path string, updated time.Duration, p model.Provider, state model.AgentState) *model.Workspace {
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: id, Name: title, NameSet: true, Branch: branch, Path: home + path, UpdatedAt: ago(updated)})
		if state != "" {
			st.Activities = append(st.Activities, model.Activity{PaneID: id + "-pane", WorkspaceID: id, Provider: p, State: state, UpdatedAt: ago(updated)})
		}
		on[id] = host
		return &st.Workspaces[len(st.Workspaces)-1]
	}
	last := func() *model.Activity { return &st.Activities[len(st.Activities)-1] }
	worker := func(parent, id, title string, p model.Provider) {
		host := on[parent]
		add(host, id, title, id, "/src/"+id, time.Minute, p, model.StateWorking).Parent = parent
	}
	if kind == "ladder" {
		add("build", "l-approval", "Migrate config loader", "config-loader", "/src/acme-api", 0, model.ProviderClaude, model.StatePendingApproval)
		last().Detail = "Run: go test ./internal/config/…"
		add("build", "l-input", "Name the release", "", "/src/acme-api", 2*time.Minute, model.ProviderCodex, model.StateAwaitingInput)
		last().Detail = "Which tag: v0.3.11 or v0.4.0?"
		add("build", "l-error", "Fix socket test", "", "/src/acme-api", 4*time.Minute, model.ProviderClaude, model.StateError)
		last().Detail = "exit status 1 · 5 repeats"
		add("build", "l-working", "Board drag polish", "board-drag", "/src/web-app", 0, model.ProviderClaude, model.StateWorking)
		add("build", "l-bg", "Fleet: release notes", "main", "/src/web-app", 6*time.Minute, model.ProviderClaude, model.StateWorking)
		last().Background = true
		add("build", "l-conn", "Starting Codex", "", "/Work", 0, model.ProviderCodex, model.StateConnecting)
		add("build", "l-watch", "Orchestrator", "main", "/src/web-app", 9*time.Minute, model.ProviderClaude, model.StateWatching)
		add("build", "l-pr", "Phone pairing QR", "pairing-qr", "/src/web-app", 12*time.Minute, model.ProviderCodex, model.StatePROpen)
		add("build", "l-run", "dev server", "", "/src/acme-api", time.Hour, model.ProviderTerminal, model.StateTerminalRunning)
		add("build", "l-plan", "Split resize plan", "main", "/src/web-app", 20*time.Minute, model.ProviderClaude, model.StatePlanReady)
		add("build", "l-done", "Theme import", "theme-import", "/src/web-app", time.Hour, model.ProviderCodex, model.StateCompleted)
		st.Stats["l-done"] = model.BranchStats{Additions: 212, Deletions: 40}
		add("build", "l-parked", "Old sweep", "main", "/src/web-app", 3*time.Hour, model.ProviderClaude, model.StateParked)
		// A tab whose command exited: Restart on its own line.
		add("build", "l-exit", "Nightly import", "", "/src/billing", 2*time.Hour, "", "")
		sb.Restartable = func(id string) bool { return id == "l-exit" }
		return st, ""
	}
	sb.HostOf = func(id string) string { return on[id] }
	sb.Hosts = []sidebar.HostInfo{{Up: true},
		{Name: "build", SSH: "dev@build", Up: true, Latency: 163 * time.Millisecond},
		{Name: "ci", SSH: "dev@ci", Up: true, Latency: 50 * time.Millisecond},
		{Name: "laptop", SSH: "dev@laptop"}}
	add("", "chat", "Chat UI design", "", "/src/web-app", 8*time.Minute, model.ProviderClaude, model.StateWorking)
	last().Background = true
	worker("chat", "chat-spinner-line", "chat-spinner-line", model.ProviderClaude)
	add("", "billing", "billing", "fix/relay-ca-bundle", "/src/billing", 48*time.Minute, model.ProviderCodex, model.StateCompleted)
	st.Stats["billing"] = model.BranchStats{Additions: 50, Deletions: 20}
	add("", "commit", "Commit all changes and clean up", "main", "/src/acme-api", time.Hour, model.ProviderCodex, model.StateCompleted)
	st.Stats["commit"] = model.BranchStats{Additions: 6706, Deletions: 1034}
	add("", "webapp", "web-app", "", "/src/web-app", 48*time.Second, model.ProviderClaude, model.StateWorking)
	last().Background = true
	worker("webapp", "api-daemon", "api-daemon", model.ProviderClaude)
	worker("webapp", "live-setup", "live-setup", model.ProviderClaude)
	worker("webapp", "test-corpus", "test-corpus", model.ProviderCodex)
	add("", "wanna", "I wanna make a project that…", "", "/Work", 52*time.Minute, model.ProviderAntigravity, model.StateCompleted)
	add("", "walk", "walk-1204", "", "/src/acme-api", 0, model.ProviderClaude, model.StateWorking).Status = "Reading logs/walk-1204/step-3…"
	add("build", "gate", "Gate A and Gate B build", "", "/src/acme-api", 35*time.Minute, model.ProviderClaude, model.StateWorking).Status = "1 shell still running"
	last().Background = true
	for i, name := range []string{"gate-a", "gate-b", "gate-c", "gate-d", "gate-e", "gate-f", "gate-g", "gate-h", "gate-i"} {
		worker("gate", name, name, model.ProviderClaude)
		if i >= 6 {
			last().State = model.StateCompleted
		}
	}
	add("build", "persist", "Persistent context design", "", "", time.Minute, model.ProviderClaude, model.StateWorking)
	add("build", "self", "acme-api", "main", "/src/acme-api", 0, "", "")
	add("ci", "ci", "dev@ci: ~", "", "", 17*time.Hour, model.ProviderTerminal, model.StateTerminalRunning)
	last().Detail = "btop"
	return st, "self"
}
