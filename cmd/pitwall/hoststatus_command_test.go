//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostRecoveryCommandQuoting(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "injected")
	argsFile := filepath.Join(dir, "args")
	// Both commands are fakes that only record argv. They never connect.
	for _, name := range []string{"ssh", "pitwall"} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsFile) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	for _, name := range []string{"box; touch " + shellQuote(marker) + " #", "-oProxyCommand=example"} {
		r := &sshHost{name: name, dest: name}
		for _, tc := range []struct{ command, prefix string }{
			{r.parseStatus("", "", context.DeadlineExceeded, true).Fix, "host\nlogin\n--\n" + name + "\n"},
			{r.install(), "--\n" + name + "\n"},
		} {
			if out, err := exec.Command("sh", "-c", tc.command).CombinedOutput(); err != nil {
				t.Fatalf("recovery command: %v: %s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("recovery command executed host text: %q", tc.command)
			}
			args, err := os.ReadFile(argsFile)
			if err != nil || !strings.HasPrefix(string(args), tc.prefix) {
				t.Fatalf("arguments %q, %v; want prefix %q", args, err, tc.prefix)
			}
		}
	}
}
