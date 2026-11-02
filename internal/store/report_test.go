package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestReportedResume(t *testing.T) {
	p := model.Pane{ID: "p", Provider: "agent:My agent", ResumeCmd: []string{"my-agent", "--resume", "a b", "a'b"}}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, model.State{Panes: []model.Pane{p}}); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil || len(s.Panes) != 1 {
		t.Fatalf("%+v, %v", s, err)
	}
	got := RestoreCmd(s.Panes[0])
	if !Resumes(s.Panes[0]) || !slices.Equal(got, p.ResumeCmd) {
		t.Fatalf("restored %q", got)
	}
	got[0] = "changed"
	if s.Panes[0].ResumeCmd[0] != "my-agent" {
		t.Fatal("RestoreCmd aliases saved argv")
	}
	p.ResumeCmd = nil
	if Resumes(p) || len(RestoreCmd(p)) != 0 {
		t.Fatal("resumed without a command")
	}
	p.Provider, p.ResumeCmd = model.ProviderClaude, []string{"wrong"}
	if Resumes(p) {
		t.Fatal("self-report command overrides built-in resume")
	}
}

func TestReportFormatMigratesNine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := `{"format_version":9,"state":{"Panes":[{"ID":"p","Provider":"codex","SessionID":"s"}]}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil || len(s.Panes) != 1 || !slices.Equal(RestoreCmd(s.Panes[0]), []string{"codex", "resume", "s"}) {
		t.Fatalf("%+v, %v", s, err)
	}
	backup, err := os.ReadFile(path + ".prev")
	if err != nil || string(backup) != data {
		t.Fatalf("backup %q, %v", backup, err)
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"format_version": 10`) {
		t.Fatalf("new format missing: %v", err)
	}
}
