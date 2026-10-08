package model

import (
	"os"
	"strings"
)

// ShortPath is p with the home directory written as ~.
func ShortPath(p string) string {
	home, _ := os.UserHomeDir()
	return shortPath(p, home)
}

func shortPath(p, home string) string {
	switch {
	case home == "" || home == "/":
		return p
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+"/"):
		return "~" + p[len(home):]
	}
	return p
}
