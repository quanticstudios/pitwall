package skillshare

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The global fixtures, captured from skillshare v0.30.7 in a temp HOME,
// and the source they describe.
//
//go:embed testdata/global testdata/home testdata/*.json
var demoFS embed.FS

// demoHome is the source folder the fixtures name.
const demoHome = "/home/demo/.config/skillshare/"

// Demo answers from the captured fixtures, for pwdemo-app and the
// screenshots: two targets, a new skill, a disabled one, an update, an
// MCP server and a hook waiting. Changes take a moment and change nothing.
func Demo() Client {
	return Client{Run: demoRun, Clone: func(context.Context, string, string, string) error { return nil },
		FS: demoFile, Dir: demoDir, Installed: func() bool { return true },
		Git: func(_ context.Context, dir string, args ...string) (string, error) {
			if args[0] == "rev-parse" {
				return strings.TrimSuffix(demoHome, "/") + "\n", nil
			}
			return "## main...origin/main [behind 2]\n", nil
		}}
}

var demoReads = map[string]string{
	"list": "list", "diff": "diff", "mcp": "mcp", "plugin": "plugins", "hooks": "hooks", "status": "status",
}

func demoRun(ctx context.Context, dir string, args ...string) Result {
	read := func(name string) Result {
		b, err := demoFS.ReadFile(name)
		if err != nil {
			return Result{Stderr: err.Error(), Code: 1}
		}
		return Result{Stdout: string(b)}
	}
	if len(args) == 0 {
		return Result{Code: 1}
	}
	switch first := args[0]; {
	case first == "check" && slices.Contains(args, "agents"):
		return read("testdata/global/check_agents.json")
	case first == "check":
		return read("testdata/global/check.json")
	case first == "install" && slices.Contains(args, "--dry-run"):
		return read("testdata/install_dryrun.json")
	case first == "install":
		return read("testdata/install_ok.json")
	case first == "audit":
		if strings.Contains(args[1], "bad-skill") {
			r := read("testdata/audit_blocked.json")
			r.Code = 1
			return r
		}
		return Result{Stdout: `{"results":[{"isBlocked":false}]}`}
	case demoReads[first] != "" && slices.Contains(args, "--json"):
		return read("testdata/global/" + demoReads[first] + ".json")
	}
	select { // a change: sync, update, enable, disable, uninstall
	case <-time.After(700 * time.Millisecond):
	case <-ctx.Done():
	}
	return Result{Stdout: "{}"}
}

func demoPath(p string) (string, error) {
	rest, ok := strings.CutPrefix(filepath.ToSlash(p), demoHome)
	if !ok {
		return "", fs.ErrNotExist
	}
	return path.Join("testdata/home", rest), nil
}

func demoFile(p string) ([]byte, error) {
	name, err := demoPath(p)
	if err != nil {
		return nil, err
	}
	return demoFS.ReadFile(name)
}

func demoDir(dir string) ([]string, error) {
	name, err := demoPath(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	err = fs.WalkDir(demoFS, name, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, strings.TrimPrefix(p, name+"/"))
		}
		return err
	})
	if len(out) == 0 && err == nil {
		err = errors.New("empty")
	}
	return out, err
}
