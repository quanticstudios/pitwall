package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
)

// A pane chat's snapshot carries the agent's / menu: Claude Code's
// skills and commands from the pane's config dir and project ahead of its
// built-ins, again once a command is added or the pane moves to another
// folder; Codex's built-ins and skills; agy's.
func TestPaneChatCommands(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	dir := t.TempDir()
	conf, proj, other := filepath.Join(dir, "conf"), filepath.Join(dir, "proj"), filepath.Join(dir, "other")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(conf, "skills", "ship", "SKILL.md"), "---\ndescription: Ship the branch\n---\n")
	write(filepath.Join(proj, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(filepath.Join(proj, ".claude", "commands", "deploy.md"), "---\ndescription: Deploy it\n---\n")
	write(filepath.Join(other, ".git", "HEAD"), "ref: refs/heads/main\n")
	transcript := filepath.Join(dir, "s.jsonl")
	write(transcript, `{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n")
	rollout, err := os.ReadFile("../session/codex/testdata/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "rollout.jsonl"), string(rollout))
	d.mu.Lock()
	d.st.Panes[0].Transcript, d.st.Panes[0].Cwd, d.st.Panes[0].Env = transcript, proj, []string{"CLAUDE_CONFIG_DIR=" + conf}
	d.st.Panes[1].Transcript = filepath.Join(dir, "rollout.jsonl")
	d.mu.Unlock()

	names := func(cs []session.Command) []string {
		var out []string
		for _, c := range cs {
			if c.Source != "" {
				out = append(out, c.Name)
			}
		}
		return out
	}
	snap, seq := paneRead(t, d, "a", session.Snapshot{}, 0)
	if got := names(snap.Commands); !slices.Equal(got, []string{"ship", "deploy"}) || len(snap.Commands) != 2+len(claude.Builtins) {
		t.Fatalf("custom commands %q of %d", got, len(snap.Commands))
	}
	if snap.Commands[0].Description != "Ship the branch" {
		t.Errorf("skill %+v", snap.Commands[0])
	}

	// A new command shows once the files are looked at again; the clock
	// stands in for slashEvery passing.
	write(filepath.Join(proj, ".claude", "commands", "fresh.md"), "fresh one\n")
	snap, seq = paneRead(t, d, "a", snap, seq)
	if got := names(snap.Commands); !slices.Equal(got, []string{"ship", "deploy"}) {
		t.Fatalf("read again within slashEvery: %q", got)
	}
	d.mu.Lock()
	pc := d.paneChats["a"]
	d.mu.Unlock()
	pc.mu.Lock()
	pc.slash.checked = time.Time{}
	pc.mu.Unlock()
	snap, seq = paneRead(t, d, "a", snap, seq)
	if got := names(snap.Commands); !slices.Equal(got, []string{"ship", "deploy", "fresh"}) {
		t.Fatalf("after a command was added: %q", got)
	}

	// The pane's folder moves to another repo: its commands go at once.
	d.mu.Lock()
	d.st.Panes[0].Cwd = other
	d.mu.Unlock()
	snap, _ = paneRead(t, d, "a", snap, seq)
	if got := names(snap.Commands); !slices.Equal(got, []string{"ship"}) {
		t.Fatalf("after cd: %q", got)
	}

	// Codex's: its built-ins, then the skills under the pane's CODEX_HOME,
	// HOME and project.
	cx := filepath.Join(dir, "cxhome")
	write(filepath.Join(cx, ".codex", "skills", "pr-review", "SKILL.md"), "---\nname: pr-review\ndescription: Review a PR\n---\n")
	write(filepath.Join(other, ".agents", "skills", "bisect", "SKILL.md"), "---\nname: bisect\ndescription: Find the commit\n---\n")
	d.mu.Lock()
	d.st.Panes[1].Cwd, d.st.Panes[1].Env = other, []string{"HOME=" + cx, "CODEX_HOME=" + filepath.Join(cx, ".codex")}
	d.mu.Unlock()
	cs, _ := paneRead(t, d, "b", session.Snapshot{}, 0)
	if got := names(cs.Commands); !slices.Equal(got, []string{"bisect", "pr-review"}) || !slices.EqualFunc(cs.Commands[:len(codex.Builtins)], codex.Builtins, session.Command.Equal) {
		t.Fatalf("codex skills %q of %d", got, len(cs.Commands))
	}

	// agy's: its built-ins and the skills under the pane's HOME and project.
	home := filepath.Join(dir, "agyhome")
	write(filepath.Join(home, ".gemini", "skills", "triage", "SKILL.md"), "---\nname: triage\ndescription: Triage the inbox\n---\n")
	write(filepath.Join(proj, ".agents", "skills", "release", "SKILL.md"), "---\nname: release\ndescription: Cut a release\n---\n")
	agyLog, err := os.ReadFile("../session/antigravity/testdata/transcript_full.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "agy.jsonl"), string(agyLog))
	d.mu.Lock()
	d.st.Panes[2].Provider, d.st.Panes[2].Transcript = model.ProviderAntigravity, filepath.Join(dir, "agy.jsonl")
	d.st.Panes[2].Cwd, d.st.Panes[2].Env = proj, []string{"HOME=" + home}
	d.mu.Unlock()
	ag, _ := paneRead(t, d, "c", session.Snapshot{}, 0)
	if got := names(ag.Commands); !slices.Equal(got, []string{"release", "triage"}) || len(ag.Commands) != 2+len(antigravity.Builtins) {
		t.Fatalf("agy skills %q of %d", got, len(ag.Commands))
	}
}

// The phone gets a pane chat's / menu after reading its entries, the
// list the window's composer gets; none for a pane that is no chat.
func TestPhoneChatMenu(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	dir := t.TempDir()
	conf := filepath.Join(dir, "conf")
	if err := os.MkdirAll(filepath.Join(conf, "skills", "ship"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "s.jsonl")
	for p, s := range map[string]string{
		filepath.Join(conf, "skills", "ship", "SKILL.md"): "---\ndescription: Ship the branch\n---\n",
		transcript: `{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"hi"}}` + "\n",
	} {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript, d.st.Panes[0].Cwd, d.st.Panes[0].Env = transcript, dir, []string{"CLAUDE_CONFIG_DIR=" + conf}
	d.mu.Unlock()
	if _, _, ok := d.phoneChatEntries("a"); !ok {
		t.Fatal("pane a is no chat")
	}
	cmds := d.phoneChatMenu("a")
	if len(cmds) != 1+len(claude.Builtins) || cmds[0].Name != "ship" || cmds[0].Source != "user" {
		t.Fatalf("phone commands %+v", cmds)
	}
	if cmds := d.phoneChatMenu("nope"); cmds != nil {
		t.Fatalf("no chat: %+v", cmds)
	}
}
