package childproc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCommandContextWorkerPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	name, suffix := "worker-tool", ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	tool := filepath.Join(bin, name+suffix)
	if err := os.WriteFile(tool, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bin, "bin"} {
		env := append(os.Environ(), "PATH=", "PATH="+path, "PATHEXT=.exe")
		cmd := CommandContext(context.Background(), dir, env, name)
		if cmd.Err != nil || cmd.Path != tool {
			t.Fatalf("worker PATH %q: %q, %v", path, cmd.Path, cmd.Err)
		}
	}
	cmd := CommandContext(context.Background(), dir, append(os.Environ(), "PATH="), name)
	if cmd.Err == nil {
		t.Fatal("an empty worker PATH found the tool")
	}
}
