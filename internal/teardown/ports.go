package teardown

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// portsPath uses the worktree's private git directory, so metadata neither
// dirties the worktree nor survives git worktree remove.
func portsPath(path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", err
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", errors.New("worktree .git has no gitdir")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return filepath.Join(dir, "pitwall-ports"), nil
}

// SavePorts keeps the assigned block available after a tab is closed.
func SavePorts(path string, ports model.PortBlock) error {
	file, err := portsPath(path)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(fmt.Sprintf("%d %d\n", ports.First, ports.Last)), 0o600)
}

// Ports reads a worktree's saved block. Worktrees without metadata have none.
func Ports(path string) (model.PortBlock, error) {
	var b model.PortBlock
	file, err := portsPath(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if _, err := fmt.Sscan(string(data), &b.First, &b.Last); err != nil {
		return b, err
	}
	if b != (model.PortBlock{}) && (b.First < 1024 || b.Last < b.First || b.Last > 65535) {
		return model.PortBlock{}, errors.New("invalid saved worktree ports")
	}
	return b, nil
}
