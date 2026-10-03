package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/quanticstudios/pitwall/internal/config"
)

const configUsage = `usage:
  pitwall config path      print the config file's path
  pitwall config default   print a commented config with every option
  pitwall config init      write that config and its schema, unless a config exists
  pitwall config check     report problems as config.toml:LINE: message
  pitwall config schema [theme]  print the JSON Schema (of a theme file)
`

// runConfig is `pitwall config <cmd>`; it returns the exit status.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, configUsage)
		return 2
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(stdout, config.Path())
	case "default":
		fmt.Fprint(stdout, config.Default())
	case "schema":
		if len(args) > 1 && args[1] == "theme" {
			stdout.Write(config.ThemeSchema())
		} else {
			stdout.Write(config.Schema())
		}
	case "check":
		s, probs := config.Load()
		for _, p := range probs {
			fmt.Fprintln(stdout, p)
		}
		for _, n := range s.Notes {
			fmt.Fprintln(stdout, n)
		}
		if len(probs) > 0 {
			return 1
		}
		fmt.Fprintln(stdout, config.Path()+": ok")
	case "init":
		path := config.Path()
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintln(stderr, "pitwall:", path, "exists; leaving it as it is")
			return 1
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "pitwall:", err)
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintln(stderr, "pitwall:", err)
			return 1
		}
		for _, f := range []struct {
			path string
			data []byte
		}{
			{config.SchemaPath(), config.Schema()},
			{config.ThemeSchemaPath(), config.ThemeSchema()},
			{path, []byte(config.Default())},
		} {
			if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
				fmt.Fprintln(stderr, "pitwall:", err)
				return 1
			}
		}
		fmt.Fprintln(stdout, "wrote", path)
	default:
		fmt.Fprint(stderr, configUsage)
		return 2
	}
	return 0
}
