package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/quanticstudios/pitwall/internal/web"
)

const maxJSONLine = 16 << 20

// jsonClient speaks pitwall's JSON-lines control protocol on $PITWALL_SOCKET
// (docs/web-protocol.md, The local socket).
type jsonClient struct {
	nc  net.Conn
	br  *bufio.Reader
	seq atomic.Int64
}

func dialJSONSocket() (net.Conn, error) {
	path, err := socketPath()
	if err != nil {
		return nil, err
	}
	nc, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(10061)) {
			return nil, errNotRunning
		}
		return nil, err
	}
	return nc, nil
}

// dialJSON connects to the local daemon socket and completes the hello/welcome
// handshake.
func dialJSON(clientName string) (*jsonClient, map[string]any, error) {
	nc, err := dialJSONSocket()
	if err != nil {
		return nil, nil, err
	}
	c := &jsonClient{nc: nc, br: bufio.NewReaderSize(nc, 64*1024)}
	if err := nc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		nc.Close()
		return nil, nil, err
	}
	if err := c.writeJSON(map[string]any{
		"type":     "hello",
		"protocol": web.Protocol,
		"client":   map[string]string{"name": clientName, "version": versionString()},
	}); err != nil {
		nc.Close()
		return nil, nil, err
	}
	_, frame, err := c.nextFrame()
	if err != nil {
		nc.Close()
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("the daemon does not speak the JSON-lines protocol; restart or update pitwall")
		}
		return nil, nil, err
	}
	if frame["type"] == "close" {
		nc.Close()
		return nil, nil, fmt.Errorf("daemon closed connection: %v", frame["reason"])
	}
	if frame["type"] != "welcome" {
		nc.Close()
		return nil, nil, fmt.Errorf("unexpected handshake reply %q", frame["type"])
	}
	_ = nc.SetDeadline(time.Time{})
	return c, frame, nil
}

func (c *jsonClient) Close() error { return c.nc.Close() }

func (c *jsonClient) nextID() string {
	return "cli-" + strconv.FormatInt(c.seq.Add(1), 10)
}

func (c *jsonClient) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.nc.Write(b)
	return err
}

func (c *jsonClient) writeRawLine(line []byte) error {
	line = bytes.TrimRight(line, "\r\n")
	_, err := c.nc.Write(append(slices.Clip(line), '\n')) // Clip: never write into the caller's spare capacity
	return err
}

// nextFrame reads the next non-blank JSON line from the daemon.
func (c *jsonClient) nextFrame() ([]byte, map[string]any, error) {
	raw, err := readJSONLine(c.br)
	if err != nil {
		return nil, nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, nil, fmt.Errorf("invalid JSON from daemon: %w", err)
	}
	return raw, m, nil
}

func readJSONLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		p, err := br.ReadSlice('\n')
		if len(line)+len(p) > maxJSONLine+1 {
			return nil, errors.New("frame exceeds 16 MiB limit")
		}
		line = append(line, p...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(bytes.TrimSpace(line)) > 0 {
				return bytes.TrimRight(line, "\r\n"), nil
			}
			return nil, err
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			line = line[:0]
			continue
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

// call sends req (assigning an id if absent) and waits for its matching result
// frame. It returns result.data on ok:true, or an error on ok:false/close/EOF.
func (c *jsonClient) call(req map[string]any, timeout time.Duration) (map[string]any, error) {
	id, _ := req["id"].(string)
	if id == "" {
		id = c.nextID()
		req["id"] = id
	}
	if timeout > 0 {
		_ = c.nc.SetDeadline(time.Now().Add(timeout))
		defer c.nc.SetDeadline(time.Time{})
	}
	if err := c.writeJSON(req); err != nil {
		return nil, err
	}
	for {
		_, frame, err := c.nextFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("the daemon closed the connection")
			}
			return nil, err
		}
		switch frame["type"] {
		case "result":
			if frame["id"] != id {
				continue
			}
			if ok, _ := frame["ok"].(bool); !ok {
				msg, _ := frame["message"].(string)
				if msg == "" {
					msg, _ = frame["code"].(string)
				}
				return nil, errors.New(msg)
			}
			data, _ := frame["data"].(map[string]any)
			return data, nil
		case "error":
			msg, _ := frame["message"].(string)
			return nil, errors.New(msg)
		case "close":
			reason, _ := frame["reason"].(string)
			if reason == "" {
				reason = "daemon exiting"
			}
			return nil, fmt.Errorf("daemon closed connection: %s", reason)
		}
	}
}

// callerSessionForJSON returns explicit if set; otherwise, when running inside
// a pitwall pane ($PITWALL_PANE) and addr is not an @-id, resolves that pane's
// session so relative addresses (#1, 1.2) act on the calling pane's session.
func callerSessionForJSON(explicit, addr string) string {
	if explicit != "" || strings.HasPrefix(addr, "@") || envCaller("").Pane == "" {
		return explicit
	}
	conn, err := dialCLI()
	if err != nil {
		return ""
	}
	defer conn.Close()
	st, err := syncCLI(conn)
	if err != nil {
		return ""
	}
	sess, err := currentSession(st, "")
	if err != nil {
		return ""
	}
	return sess.ID
}
