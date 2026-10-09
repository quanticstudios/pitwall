package app

import (
	"bytes"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
)

// TestToasterReadsLines runs the toaster's loop in Windows PowerShell with
// Show-Toast swapped for one that prints its arguments: each line on stdin
// reaches it whole, non-ASCII text included.
func TestToasterReadsLines(t *testing.T) {
	echo := "function Show-Toast($title, $body, $tag) { [Console]::Out.WriteLine([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(\"$title|$body|$tag\"))) }\n"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toastHead+echo+toastLoop)
	cmd.Stdin = bytes.NewReader(append(toastLine("café ✳", "two\nlines", "ws1"), toastLine("next", "", "")...))
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	var got []string
	for _, l := range strings.Fields(out.String()) {
		b, err := base64.StdEncoding.DecodeString(l)
		if err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		got = append(got, string(b))
	}
	if strings.Join(got, ";") != "café ✳|two\nlines|ws1;next||" {
		t.Fatalf("Show-Toast got %q; stderr %s", got, errOut.String())
	}
}
