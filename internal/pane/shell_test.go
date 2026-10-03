package pane

import (
	"errors"
	"slices"
	"testing"
)

func TestWindowsShell(t *testing.T) {
	for _, tc := range []struct {
		shell string
		path  []string
		want  string
	}{
		{"bash.exe", []string{"bash.exe", "pwsh.exe"}, "bash.exe"},
		{"/usr/bin/bash", []string{"pwsh.exe", "powershell.exe"}, "pwsh.exe"}, // Git Bash's $SHELL does not run
		{"", []string{"powershell.exe"}, "powershell.exe"},
		{"", nil, "cmd.exe"},
	} {
		look := func(s string) (string, error) {
			if slices.Contains(tc.path, s) {
				return `C:\bin\` + s, nil
			}
			return "", errors.New("not found")
		}
		if got := windowsShell(tc.shell, look); got != tc.want {
			t.Errorf("windowsShell(%q) with %v = %q, want %q", tc.shell, tc.path, got, tc.want)
		}
	}
}
