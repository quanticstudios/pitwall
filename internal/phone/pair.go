// Package phone is the Android app's side of the phone page's API
// (internal/remote): pairing, the saved device token, the event stream
// with its reconnects, a chat's entries as they page and stream in, and
// the model.State the sidebar draws from the tab list.
package phone

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Creds is what pairing leaves on the phone.
type Creds struct {
	Server string `json:"server"` // the page's address, ending in "/"
	Token  string `json:"token"`
	Device string `json:"device,omitempty"` // the name pitwall gave this phone
	// Pin is the SHA-256 of a certificate the user chose to trust, as
	// pitwall remote pair prints it; "" trusts the system's authorities.
	Pin string `json:"pin,omitempty"`
}

const credsFile = "pairing.json"

// Load reads the saved pairing in dir; an error wrapping os.ErrNotExist
// means the phone is not paired.
func Load(dir string) (Creds, error) {
	var c Creds
	data, err := os.ReadFile(filepath.Join(dir, credsFile))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.Server == "" || c.Token == "" {
		return c, os.ErrNotExist
	}
	return c, nil
}

// Save writes c to dir, mode 0600, so a crash never leaves half a file.
func Save(dir string, c Creds) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pairing-*")
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
	return os.Rename(tmp.Name(), filepath.Join(dir, credsFile)) // CreateTemp made it 0600
}

// Forget removes the saved pairing.
func Forget(dir string) error {
	err := os.Remove(filepath.Join(dir, credsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

var errNoCode = errors.New("paste the link pitwall remote pair prints, or the address and the code")

// ParseInvite reads what the user typed: the link pitwall remote pair
// prints ("https://box.example.com/#pair=CODE"), or the page's address
// with the code in code. An address without a scheme gets https.
func ParseInvite(link, code string) (server, pairCode string, err error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return "", "", errNoCode
	}
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", "", errors.New("that is not an http or https address")
	}
	if frag, _ := url.ParseQuery(u.Fragment); frag.Get("pair") != "" && strings.TrimSpace(code) == "" {
		code = frag.Get("pair")
	}
	u.Fragment, u.RawFragment, u.RawQuery = "", "", ""
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	// why: the page takes the code as its base32 letters only, as people misread or space them.
	code = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '2' && r <= '7':
			return r
		}
		return -1
	}, code)
	if code == "" {
		return "", "", errNoCode
	}
	return u.String(), code, nil
}

// SamePin reports whether two SHA-256 fingerprints match, whatever their
// case, colons or spaces.
func SamePin(a, b string) bool {
	norm := func(s string) string {
		return strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(s))
	}
	return a != "" && norm(a) == norm(b)
}
