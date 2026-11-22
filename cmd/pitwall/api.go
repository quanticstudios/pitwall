package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quanticstudios/pitwall/internal/web"
)

const apiUsage = `usage: pitwall api [json...]

Escape hatch: send raw JSON-lines commands to the pitwall daemon socket
($PITWALL_SOCKET) and print every reply line on stdout.

Commands come from positional arguments, or from stdin when no arguments (or
"-") are given. Unless the first frame is already a "hello", pitwall sends
the protocol handshake automatically and prints only replies to your commands.
If a command object omits "id", pitwall assigns one. Subscriptions
(subscribeTabs, subscribeEvents, ...) stream until Ctrl+C or daemon close.

Examples:
  pitwall api '{"type":"listTabs","id":"1"}'
  printf '%s\n' '{"type":"listTabs"}' '{"type":"queueList"}' | pitwall api

Flags:
  -h, --help  show this help`

type apiOptions struct {
	args  []string
	stdin bool
	help  bool
}

func parseAPIArgs(args []string) (apiOptions, error) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		return apiOptions{help: true}, nil
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
		return apiOptions{stdin: true}, nil
	}
	return apiOptions{args: args}, nil
}

func apiCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAPI(ctx, args, in, out, errOut)
}

func runAPI(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	opt, err := parseAPIArgs(args)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	if opt.help {
		_, _ = io.WriteString(out, apiUsage+"\n")
		return 0
	}

	var src io.Reader
	if opt.stdin {
		if in == nil {
			fmt.Fprintln(errOut, "pitwall: no input on stdin")
			return 1
		}
		src = in
	} else {
		src = strings.NewReader(strings.Join(opt.args, "\n") + "\n")
	}

	nc, err := dialJSONSocket()
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	c := &jsonClient{nc: nc, br: bufio.NewReaderSize(nc, 64*1024)}
	defer c.Close()

	stopConn := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopConn()

	inReader := bufio.NewReader(src)
	firstLine, err := readJSONLine(inReader)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0
		}
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}

	userHello := isHelloFrame(firstLine)
	if userHello {
		if err := c.writeRawLine(firstLine); err != nil {
			fmt.Fprintln(errOut, "pitwall:", err)
			return 1
		}
		raw, frame, err := c.nextFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(errOut, "pitwall: the daemon does not speak the JSON-lines protocol; restart or update pitwall")
			} else {
				fmt.Fprintln(errOut, "pitwall:", err)
			}
			return 1
		}
		if _, err := out.Write(append(raw, '\n')); err != nil {
			fmt.Fprintln(errOut, "pitwall:", err)
			return 1
		}
		if frame["type"] == "close" {
			return 1
		}
	} else {
		_ = nc.SetDeadline(time.Now().Add(5 * time.Second))
		if err := c.writeJSON(map[string]any{
			"type":     "hello",
			"protocol": web.Protocol,
			"client":   map[string]string{"name": "pitwall-api", "version": versionString()},
		}); err != nil {
			fmt.Fprintln(errOut, "pitwall:", err)
			return 1
		}
		_, frame, err := c.nextFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(errOut, "pitwall: the daemon does not speak the JSON-lines protocol; restart or update pitwall")
			} else {
				fmt.Fprintln(errOut, "pitwall:", err)
			}
			return 1
		}
		if frame["type"] == "close" {
			fmt.Fprintf(errOut, "pitwall: daemon closed connection: %v\n", frame["reason"])
			return 1
		}
		_ = nc.SetDeadline(time.Time{})
	}

	var mu sync.Mutex
	pending := map[string]string{} // id -> command type
	untagged := 0                  // malformed/non-id frames expecting an error frame
	subs := map[string]bool{}      // active subscriptions by base name ("tabs", "events", ...)
	inputDone := false
	doneWake := make(chan struct{}, 1)
	signalDone := func() {
		select {
		case doneWake <- struct{}{}:
		default:
		}
	}
	shouldStop := func() bool {
		return inputDone && len(pending) == 0 && untagged == 0 && len(subs) == 0
	}

	var writeErr error
	var sendID int64
	sendLine := func(line []byte) error {
		prepared, id, typ, hasID := prepareAPILine(line, &sendID)
		mu.Lock()
		if hasID {
			pending[id] = typ
		} else {
			untagged++
		}
		mu.Unlock()
		return c.writeRawLine(prepared)
	}

	// Send first line if it wasn't hello.
	if !userHello {
		if err := sendLine(firstLine); err != nil {
			fmt.Fprintln(errOut, "pitwall:", err)
			return 1
		}
	}

	// Writer goroutine reads remaining lines from src.
	go func() {
		for {
			line, err := readJSONLine(inReader)
			if err != nil {
				mu.Lock()
				if !errors.Is(err, io.EOF) {
					writeErr = err
				}
				inputDone = true
				stop := shouldStop()
				mu.Unlock()
				if stop {
					_ = c.Close()
				}
				signalDone()
				return
			}
			if err := sendLine(line); err != nil {
				mu.Lock()
				writeErr = err
				inputDone = true
				mu.Unlock()
				_ = c.Close()
				signalDone()
				return
			}
		}
	}()

	for {
		raw, frame, err := c.nextFrame()
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			mu.Lock()
			done := shouldStop()
			werr := writeErr
			mu.Unlock()
			if done && werr == nil {
				return 0
			}
			if werr != nil {
				fmt.Fprintln(errOut, "pitwall:", werr)
				return 1
			}
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(errOut, "pitwall: daemon connection lost")
			} else {
				fmt.Fprintln(errOut, "pitwall:", err)
			}
			return 1
		}
		if _, err := out.Write(append(raw, '\n')); err != nil {
			fmt.Fprintln(errOut, "pitwall:", err)
			return 1
		}
		if frame["type"] == "close" {
			if ctx.Err() != nil {
				return 0
			}
			return 1
		}
		mu.Lock()
		switch frame["type"] {
		case "result":
			id, _ := frame["id"].(string)
			typ, ok := pending[id]
			if ok {
				delete(pending, id)
				if resOK, _ := frame["ok"].(bool); resOK {
					if rest, isSub := strings.CutPrefix(typ, "subscribe"); isSub {
						subs[rest] = true
					} else if rest, isUnsub := strings.CutPrefix(typ, "unsubscribe"); isUnsub {
						delete(subs, rest)
					}
				}
			}
		case "error", "pong":
			if untagged > 0 {
				untagged--
			}
		}
		stop := shouldStop()
		mu.Unlock()
		if stop {
			return 0
		}
	}
}

func isHelloFrame(line []byte) bool {
	var f struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(line, &f) == nil && f.Type == "hello"
}

// prepareAPILine preserves line verbatim when it already has an "id" (or isn't
// a JSON object with a "type"), and assigns an automatic "id" otherwise so
// one-liners like `pitwall api '{"type":"listTabs"}'` work cleanly.
func prepareAPILine(line []byte, seq *int64) (out []byte, id, typ string, hasID bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(line, &obj) != nil || obj == nil {
		return line, "", "", false
	}
	if rawTyp, ok := obj["type"]; ok {
		_ = json.Unmarshal(rawTyp, &typ)
	}
	if rawID, ok := obj["id"]; ok {
		if json.Unmarshal(rawID, &id) == nil && id != "" {
			return line, id, typ, true
		}
	}
	if typ == "" {
		return line, "", "", false
	}
	*seq++
	id = "api-" + strconv.FormatInt(*seq, 10)
	obj["id"], _ = json.Marshal(id)
	b, err := json.Marshal(obj)
	if err != nil {
		return line, "", typ, false
	}
	return bytes.TrimSpace(b), id, typ, true
}
