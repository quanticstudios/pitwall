package gitstat

import (
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInheritedGitDirLeavesRepositoryUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	mustPlain(t, repo, "init", "--bare", "-q")
	snapshot := func() map[string]string {
		t.Helper()
		files := map[string]string{}
		err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			files[path] = "directory"
			if !d.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = string(data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	before := snapshot()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSubmoduleConfigRunsNothing$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repo, "GIT_CONFIG="+filepath.Join(repo, "config"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("test with inherited GIT_DIR: %v\n%s", err, out)
	}
	if !maps.Equal(before, snapshot()) {
		t.Error("test with inherited GIT_DIR changed the scratch repository")
	}
}
