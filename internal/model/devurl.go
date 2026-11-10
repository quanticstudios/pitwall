package model

import (
	"path/filepath"
	"strconv"
	"strings"
)

// DevHost is the worktree's branch.repo.localhost name, or empty without ports.
func (w Workspace) DevHost() string {
	if w.Ports.First == 0 || w.WorktreeRoot == "" || w.Branch == "" {
		return ""
	}
	return dnsLabel(w.Branch) + "." + dnsLabel(filepath.Base(w.WorktreeRoot)) + ".localhost"
}

// DevURL is the worktree's URL on proxyPort, or empty when the proxy is off.
func (w Workspace) DevURL(proxyPort int) string {
	if host := w.DevHost(); proxyPort > 0 && host != "" {
		return "http://" + host + ":" + strconv.Itoa(proxyPort)
	}
	return ""
}

func dnsLabel(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() == 63 {
			break
		}
	}
	if label := strings.TrimRight(b.String(), "-"); label != "" {
		return label
	}
	return "worktree"
}
