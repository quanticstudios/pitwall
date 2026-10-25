package timelog

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Kinds of time in a turn: the model, waiting on the user, and each kind
// of tool call.
const (
	Model = "model"
	Wait  = "wait"

	// Shell commands.
	Test    = "test"
	Build   = "build"
	Lint    = "lint" // and format
	Git     = "git"
	CI      = "ci" // gh, and sleeps and polls
	Install = "install"
	Shell   = "shell" // any other command

	// Other tools.
	Search = "read" // reading and searching files
	Edit   = "edit"
	Agent  = "agent" // a subagent
	Web    = "web"
	MCP    = "mcp"
	Tool   = "tool" // any other tool
)

// Kinds is every kind, in the order a report lists them.
var Kinds = []string{Model, Test, Build, CI, Lint, Git, Install, Shell, Search, Edit, Agent, Web, MCP, Tool, Wait}

// Label names kind for a person.
func Label(kind string) string {
	switch kind {
	case Model:
		return "Model"
	case Wait:
		return "Waiting on you"
	case Test:
		return "Tests"
	case Build:
		return "Builds"
	case Lint:
		return "Lint and format"
	case Git:
		return "Git"
	case CI:
		return "CI and gh"
	case Install:
		return "Installs"
	case Shell:
		return "Other commands"
	case Search:
		return "Read and search"
	case Edit:
		return "Edits"
	case Agent:
		return "Subagents"
	case Web:
		return "Web"
	case MCP:
		return "MCP"
	}
	return "Other tools"
}

// shellTools are the names agents give their shell tool.
var shellTools = []string{"bash", "shell", "run_shell_command", "exec_command", "run_terminal_cmd", "terminal"}

// Classify is the kind of a call to tool with command, the command a
// shell tool ran ("" for other tools).
func Classify(tool, command string) string {
	t := strings.ToLower(tool)
	if command != "" || slices.Contains(shellTools, t) {
		return Command(command)
	}
	switch {
	case strings.HasPrefix(t, "mcp__") || strings.HasPrefix(t, "mcp_"):
		return MCP
	case slices.Contains([]string{"read", "grep", "glob", "ls", "list", "view", "find", "search", "read_file", "read_many_files", "search_file_content", "list_directory", "codebase_search", "notebookread"}, t):
		return Search
	case slices.Contains([]string{"edit", "multiedit", "write", "notebookedit", "apply_patch", "patch", "write_file", "replace", "str_replace", "search_replace", "delete_file"}, t):
		return Edit
	case slices.Contains([]string{"task", "agent", "subagent"}, t):
		return Agent
	case slices.Contains([]string{"webfetch", "websearch", "web_fetch", "web_search", "google_web_search", "fetch"}, t):
		return Web
	}
	return Tool
}

// segments splits a command line where one program ends and another may
// start.
var segments = regexp.MustCompile("&&|\\|\\||[;|\n(){}`]|\\$\\(")

// Ranks: a compound command is the kind of its highest-ranked part, so
// "go build && go test" is a test and "sleep 30; gh pr checks" CI. A
// sleep alone is a poll, CI, but below everything that does work.
var rank = map[string]int{CI: 7, Test: 6, Build: 5, Lint: 4, Install: 3, "sleep": 2, Git: 1}

// Command is the kind of a shell command line.
// ponytail: a word list per tool; a command it does not know is Shell.
func Command(line string) string {
	best := Shell
	for _, seg := range segments.Split(line, -1) {
		k := program(strings.Fields(seg))
		if rank[k] > rank[best] {
			best = k
		}
	}
	if best == "sleep" {
		return CI
	}
	return best
}

// wrappers run the program after them, with flags of their own.
var wrappers = []string{"do", "then", "else", "if", "elif", "while", "until", "!", "time", "sudo", "exec", "command", "nohup", "nice", "xargs", "builtin", "noglob", "rtk", "npx", "bunx", "pnpx"}

// program is the kind of one simple command, its words in w.
func program(w []string) string {
	for len(w) > 0 {
		switch x := w[0]; {
		case strings.Contains(x, "=") && !strings.HasPrefix(x, "-"): // FOO=bar
			w = w[1:]
		case slices.Contains(wrappers, x):
			w = w[1:]
		case x == "env" || x == "timeout":
			w = w[1:]
			for len(w) > 0 && (strings.HasPrefix(w[0], "-") || strings.Contains(w[0], "=") || x == "timeout" && w[0] != "" && w[0][0] >= '0' && w[0][0] <= '9') {
				if w[0] == "-u" && len(w) > 1 {
					w = w[1:]
				}
				w = w[1:]
			}
		case x == "mise" && len(w) > 1 && w[1] == "exec":
			i := slices.Index(w, "--")
			if i < 0 {
				return Shell
			}
			w = w[i+1:]
		case (x == "uv" || x == "poetry" || x == "pipenv" || x == "bundle") && len(w) > 1 && (w[1] == "run" || w[1] == "exec"):
			w = w[2:]
		default:
			return kind(strings.ToLower(filepath.Base(x)), w[1:])
		}
	}
	return Shell
}

func kind(prog string, args []string) string {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch prog {
	case "gh":
		return CI
	case "sleep":
		return "sleep"
	case "git":
		return Git
	case "go":
		switch sub {
		case "test":
			return Test
		case "build", "install", "generate":
			return Build
		case "vet", "fmt":
			return Lint
		case "mod", "get":
			return Install
		}
		return Shell
	case "cargo":
		switch sub {
		case "test", "nextest", "bench":
			return Test
		case "build", "check":
			return Build
		case "clippy", "fmt":
			return Lint
		case "add", "fetch", "install", "update":
			return Install
		}
		return Shell
	case "npm", "pnpm", "yarn", "bun", "deno":
		switch sub {
		case "test", "t":
			return Test
		case "install", "i", "ci", "add", "update", "upgrade":
			return Install
		case "run", "run-script":
			if len(args) > 1 {
				return script(args[1])
			}
			return Shell
		case "":
			if prog == "yarn" {
				return Install
			}
		}
		return script(sub)
	case "make", "just", "task":
		if sub == "" || strings.HasPrefix(sub, "-") {
			return Build
		}
		if k := script(sub); k != Shell {
			return k
		}
		return Build
	case "mise":
		switch sub {
		case "install":
			return Install
		case "run", "r":
			if len(args) > 1 {
				return script(args[1])
			}
		}
		return Shell
	case "python", "python3":
		if sub == "-m" && len(args) > 1 {
			return kind(args[1], args[2:])
		}
		if sub != "" {
			return script(sub)
		}
		return Shell
	case "sh", "bash", "zsh":
		if sub == "-c" || sub == "-lc" {
			return Command(strings.Trim(strings.Join(args[1:], " "), `'"`))
		}
		if sub != "" && !strings.HasPrefix(sub, "-") {
			return script(sub)
		}
		return Shell
	case "pytest", "unittest", "jest", "vitest", "mocha", "ava", "rspec", "phpunit", "ctest", "tox", "nox", "gotestsum", "ginkgo", "cypress", "karma":
		return Test
	case "playwright":
		if sub == "test" {
			return Test
		}
		return Shell
	case "tsc":
		if slices.Contains(args, "--noEmit") {
			return Lint
		}
		return Build
	case "vite", "webpack", "rollup", "esbuild", "turbo", "next", "gradle", "gradlew", "mvn", "bazel", "dotnet", "swift", "zig", "docker":
		switch sub {
		case "test":
			return Test
		case "build", "package", "compile", "bundle", "":
			return Build
		}
		if prog == "turbo" || prog == "gradle" || prog == "gradlew" {
			return script(sub)
		}
		return Shell
	case "gcc", "g++", "clang", "clang++", "cc", "javac", "cmake", "ninja", "msbuild", "xcodebuild":
		return Build
	case "gofmt", "goimports", "golangci-lint", "staticcheck", "eslint", "prettier", "biome", "ruff", "black", "isort", "flake8", "pylint",
		"mypy", "pyright", "shellcheck", "shfmt", "rustfmt", "stylelint", "markdownlint", "actionlint", "hadolint", "yamllint", "swiftlint", "ktlint", "rubocop":
		return Lint
	case "pip", "pip3", "uv", "poetry", "brew", "apt", "apt-get", "dnf", "pacman", "yay", "asdf", "gem", "composer", "bundle":
		if prog == "uv" && sub == "build" {
			return Build
		}
		return Install
	}
	if strings.Contains(prog, ".") || strings.Contains(prog, "-") || strings.Contains(prog, "_") {
		return script(prog) // ./scripts/test.sh, run-tests
	}
	return Shell
}

// script is the kind a script or task name says: get_test.sh is a test,
// build:web a build.
func script(name string) string {
	n := strings.ToLower(filepath.Base(name))
	switch {
	case strings.Contains(n, "test") || strings.Contains(n, "e2e") || strings.Contains(n, "spec"):
		return Test
	case strings.Contains(n, "lint") || strings.Contains(n, "fmt") || strings.Contains(n, "format") || strings.Contains(n, "typecheck") || strings.Contains(n, "check") || strings.Contains(n, "vet"):
		return Lint
	case strings.Contains(n, "build") || strings.Contains(n, "compile") || strings.Contains(n, "bundle") || strings.Contains(n, "release"):
		return Build
	case strings.Contains(n, "install") || strings.Contains(n, "setup") || strings.Contains(n, "bootstrap"):
		return Install
	}
	return Shell
}
