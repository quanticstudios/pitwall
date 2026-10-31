// Package web serves the web protocol (docs/web-protocol.md) to local web
// clients such as aide: a WebSocket on 127.0.0.1, and web.json, the file
// in the state folder that tells a client the address and the token.
//
// web.json is mode 0600 on Unix. On Windows the mode bits do nothing; the
// file is as private as %LOCALAPPDATA%, which only the user (and the
// system and administrators) can read.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/quanticstudios/pitwall/internal/config"
)

// File is web.json.
type File struct {
	URL   string `json:"url"`   // ws://127.0.0.1:<port>/ws
	Token string `json:"token"` // 64 hex digits
	PID   int    `json:"pid"`   // the daemon serving URL, so a client can tell a stale file
}

var tokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Path is web.json in the state folder.
func Path() string { return filepath.Join(config.StateDir(), "web.json") }

// Listen listens on 127.0.0.1 only, at port, or at a port the system
// picks for 0.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// Read reads web.json at path.
func Read(path string) (File, error) {
	var f File
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

// Publish writes web.json for the daemon pid listening at addr. It keeps
// the token already there, so clients survive a restart, and makes one
// when there is none.
func Publish(path string, addr net.Addr, pid int) (File, error) {
	f, _ := Read(path) // a missing or damaged file gets a new token
	f.URL, f.PID = "ws://"+addr.String()+"/ws", pid
	if !tokenRe.MatchString(f.Token) {
		f.Token = newToken()
	}
	return f, write(path, f)
}

// Rotate gives web.json a new token. A running daemon checks every hello
// against the file, so the old token stops working at once.
func Rotate(path string) (File, error) {
	f, _ := Read(path)
	f.Token = newToken()
	return f, write(path, f)
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b) // never fails; see crypto/rand.Read
	return hex.EncodeToString(b)
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// write writes f to path with mode 0600, atomically.
func write(path string, f File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// writeFile writes data to path with mode 0600, atomically.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".web-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // CreateTemp made it 0600
}
