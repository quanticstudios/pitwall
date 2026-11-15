package childproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CommandContext resolves name using env's PATH, relative to dir. A nil
// env keeps exec.CommandContext's normal lookup and inherited environment.
func CommandContext(ctx context.Context, dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	if env == nil {
		return cmd
	}
	get := func(key string) string {
		for i := len(env) - 1; i >= 0; i-- {
			k, v, _ := strings.Cut(env[i], "=")
			if k == key || runtime.GOOS == "windows" && strings.EqualFold(k, key) {
				return v
			}
		}
		return ""
	}
	// Preserve the daemon's normal lookup, including exec.ErrDot.
	if get("PATH") == os.Getenv("PATH") && (runtime.GOOS != "windows" || get("PATHEXT") == os.Getenv("PATHEXT")) {
		return cmd
	}
	exts := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		exts = strings.Split(get("PATHEXT"), ";")
		if len(exts) == 1 && exts[0] == "" {
			exts = []string{".com", ".exe", ".bat", ".cmd"}
		}
	}
	for _, entry := range filepath.SplitList(get("PATH")) {
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(dir, entry)
		}
		for _, ext := range exts {
			candidate, err := filepath.Abs(filepath.Join(entry, name+ext))
			if err != nil {
				continue
			}
			if path, err := exec.LookPath(candidate); err == nil {
				cmd.Path, cmd.Err = path, nil
				return cmd
			}
		}
	}
	cmd.Err = &exec.Error{Name: name, Err: exec.ErrNotFound}
	return cmd
}
