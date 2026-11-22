package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
)

const themeUsage = `usage:
  pitwall theme import [--name <name>] [--apply] <file|dir|name>...
                           import a Ghostty theme file, an iTerm2 .itermcolors
                           file, a directory of themes, or an installed Ghostty
                           theme by name into the config dir
  pitwall theme list       list built-in and custom themes
`

// runTheme is `pitwall theme <cmd>`; it returns the exit status.
func runTheme(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, themeUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runThemeImport(args[1:], stdout, stderr)
	case "list", "ls":
		dir := filepath.Join(config.Dir(), "themes")
		for _, t := range config.AllThemes(dir) {
			if t.Custom {
				fmt.Fprintf(stdout, "%s\tcustom\n", t.Name)
			} else {
				fmt.Fprintln(stdout, t.Name)
			}
		}
		return 0
	default:
		fmt.Fprint(stderr, themeUsage)
		return 2
	}
}

func runThemeImport(args []string, stdout, stderr io.Writer) int {
	var customName string
	var apply bool
	var sources []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			sources = append(sources, args[i+1:]...)
			i = len(args)
		case a == "--apply":
			apply = true
		case a == "--name":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "pitwall theme import: --name requires a value")
				return 2
			}
			i++
			customName = args[i]
		case strings.HasPrefix(a, "--name="):
			customName = strings.TrimPrefix(a, "--name=")
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, themeUsage)
			return 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "pitwall theme import: unknown flag %q\n%s", a, themeUsage)
			return 2
		default:
			sources = append(sources, a)
		}
	}
	if len(sources) == 0 {
		fmt.Fprint(stderr, themeUsage)
		return 2
	}
	if customName != "" && len(sources) > 1 {
		fmt.Fprintln(stderr, "pitwall theme import: --name can only be used with a single source")
		return 2
	}
	themesDir := filepath.Join(config.Dir(), "themes")
	var imported []config.ImportedTheme
	for _, src := range sources {
		res, err := config.ImportTheme(src, customName, themesDir)
		if err != nil {
			fmt.Fprintf(stderr, "pitwall theme import: %v\n", err)
			return 1
		}
		for _, item := range res {
			fmt.Fprintf(stdout, "imported %s (%s)\n", item.Name, item.Path)
			imported = append(imported, item)
		}
	}
	if apply && len(imported) == 1 {
		if err := config.SetKey(config.Path(), "theme", "name", config.Quote(imported[0].Name)); err != nil {
			fmt.Fprintf(stderr, "pitwall theme import: saved theme but could not update config: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "applied %s\n", imported[0].Name)
	}
	return 0
}
