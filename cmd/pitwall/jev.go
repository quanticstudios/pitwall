package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
)

const jevUsage = `usage:
  pitwall jev login    save a TypeSafe API key and use Jev for decisions
                       (reads the key without echo, or from stdin when piped)
  pitwall jev status   test the connection and show what is on
  pitwall jev logout   remove the saved key and turn decisions off
Get a key at ` + decide.KeysURL + `
`

// jevPing makes one real call; tests replace it.
var jevPing = func(ctx context.Context, key, model string) (time.Duration, error) {
	return decide.Ping(ctx, decide.NewJev(key, model), 10*time.Second, key)
}

// readSecret reads a key from the terminal without echo, or the first
// line of stdin when it is not a terminal; tests replace it.
var readSecret = func(stdin io.Reader, prompt io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(f.Fd()) {
		fmt.Fprint(prompt, "TypeSafe API key (input hidden): ")
		b, err := term.ReadPassword(f.Fd())
		fmt.Fprintln(prompt)
		return string(b), err
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err == io.EOF {
		err = nil
	}
	return line, err
}

func runJev(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, jevUsage)
		return 2
	}
	cred := decide.CredentialsPath(config.Dir())
	fail := func(err error) int {
		fmt.Fprintln(stderr, "pitwall:", decide.Redact(err.Error()))
		return 1
	}
	switch args[0] {
	case "login":
		fmt.Fprintln(stderr, "Get a key at", decide.KeysURL)
		key, err := readSecret(stdin, stderr)
		if err != nil {
			return fail(err)
		}
		key = strings.TrimSpace(key)
		if err := decide.CheckKey(key); err != nil {
			return fail(err)
		}
		if err := decide.SaveKey(cred, key); err != nil {
			return fail(err)
		}
		if runtime.GOOS == "windows" {
			fmt.Fprintln(stdout, "Saved the key in", cred, "(it has your profile folder's permissions; pitwall sets no ACL).")
		} else {
			fmt.Fprintln(stdout, "Saved the key in", cred, "(mode 0600, readable only by you).")
		}
		if s := config.LoadDecisions(config.Path()); s.Provider != "jev" {
			if err := config.SetKey(config.Path(), "decisions", "provider", config.Quote("jev")); err != nil {
				return fail(err)
			}
		}
		fmt.Fprintln(stdout, "Decisions use Jev now. Feature settings in config.toml are kept; the features line below shows what is on.")
		if os.Getenv(decide.KeyEnv) != "" {
			fmt.Fprintln(stdout, "Note: "+decide.KeyEnv+" is set and wins over the saved key.")
		}
		return jevStatus(stdout, stderr)
	case "status":
		return jevStatus(stdout, stderr)
	case "logout":
		if err := decide.DeleteKey(cred); err != nil {
			return fail(err)
		}
		switch s := config.LoadDecisions(config.Path()); s.Provider {
		case "jev":
			if err := config.SetKey(config.Path(), "decisions", "provider", config.Quote("")); err != nil {
				return fail(err)
			}
			fmt.Fprintln(stdout, "Removed the saved key and set provider = \"\". Decisions are off; nothing is sent.")
		case "command":
			fmt.Fprintln(stdout, "Removed the saved Jev key. Decisions still run through your command ("+s.Command[0]+"); set provider = \"\" under [decisions] to turn them off.")
		default:
			fmt.Fprintln(stdout, "Removed the saved key. Decisions were already off.")
		}
		if os.Getenv(decide.KeyEnv) != "" {
			fmt.Fprintln(stdout, decide.KeyEnv+" is still set in this environment; unset it as well.")
		}
		return 0
	}
	fmt.Fprint(stderr, jevUsage)
	return 2
}

// jevStatus tests the key with one real call and prints what is on. It
// never prints the key.
func jevStatus(stdout, stderr io.Writer) int {
	s := config.LoadDecisions(config.Path())
	key, src, err := decide.LoadKey(decide.CredentialsPath(config.Dir()))
	if err != nil {
		fmt.Fprintln(stderr, "pitwall:", err)
		return 1
	}
	switch s.Provider {
	case "jev":
		fmt.Fprintln(stdout, "provider:   jev ("+s.Model+")")
	case "command":
		fmt.Fprintln(stdout, "provider:   command ("+s.Command[0]+")")
	default:
		fmt.Fprintln(stdout, "provider:   none; nothing is sent")
	}
	if key == "" {
		fmt.Fprintln(stdout, "key:        none (pitwall jev login)")
		if s.Provider == "jev" {
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "key:        from the "+src)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d, err := jevPing(ctx, key, s.Model)
	if err != nil {
		fmt.Fprintln(stdout, "connection: failed:", decide.Redact(err.Error(), key))
		return 1
	}
	fmt.Fprintf(stdout, "connection: ok, %d ms\n", d.Milliseconds())
	if s.On() {
		onOff := map[bool]string{true: "on", false: "off"}
		fmt.Fprintf(stdout, "features:   approvals %s, triage %s, agents %s, turn check %s\n", s.Approvals, onOff[s.Triage], onOff[s.Agents], onOff[s.TurnCheck])
	}
	return 0
}
