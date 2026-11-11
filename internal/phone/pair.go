// Package phone is the Android app's side of the phone page's API
// (internal/remote): pairing, the paired machines, the event stream
// with its reconnects, a chat's entries as they page and stream in, and
// the model.State the sidebar draws from the machines' tab lists.
package phone

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// Machine is one paired computer: the name the app shows it by, and its
// pairing.
type Machine struct {
	ID   string `json:"id"` // made at pairing; names the machine in ids and routes
	Name string `json:"name"`
	Creds
}

const machinesFile = "machines.json"

// LoadMachines reads the paired machines saved in dir, none when the
// phone is not paired.
func LoadMachines(dir string) ([]Machine, error) {
	var ms []Machine
	data, err := os.ReadFile(filepath.Join(dir, machinesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &ms); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ms, func(m Machine) bool { return m.ID == "" || m.Server == "" || m.Token == "" }), nil
}

// SaveMachines writes ms to dir, mode 0600, so a crash never leaves half a
// file.
func SaveMachines(dir string, ms []Machine) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(ms)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".machines-*")
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
	return os.Rename(tmp.Name(), filepath.Join(dir, machinesFile)) // CreateTemp made it 0600
}

// AddMachine is ms with c's machine: in place of one paired with the same
// server before, keeping its name, else at the end, named after the
// server's host with a number when another machine has that name.
func AddMachine(ms []Machine, c Creds) ([]Machine, Machine) {
	if i := slices.IndexFunc(ms, func(m Machine) bool { return m.Server == c.Server }); i >= 0 {
		ms = slices.Clone(ms)
		ms[i].Creds = c
		return ms, ms[i]
	}
	b := make([]byte, 6)
	rand.Read(b)
	m := Machine{ID: hex.EncodeToString(b), Name: UniqueName(ms, HostName(c.Server), ""), Creds: c}
	return append(slices.Clone(ms), m), m
}

// UniqueName is name, or name-2, name-3 and so on when another machine
// than id has it.
func UniqueName(ms []Machine, name, id string) string {
	taken := func(n string) bool {
		return slices.ContainsFunc(ms, func(m Machine) bool { return m.ID != id && strings.EqualFold(m.Name, n) })
	}
	for i, n := 2, name; ; i++ {
		if !taken(n) {
			return n
		}
		n = name + "-" + strconv.Itoa(i)
	}
}

// HostName is how the app first names a server: its host's first name
// ("box" of box.example.com), or its address.
func HostName(server string) string {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return server
	}
	h := u.Hostname()
	if net.ParseIP(h) != nil {
		return h
	}
	first, _, _ := strings.Cut(h, ".")
	return first
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

// ErrNotPairing is ParseScan's error for a QR code that holds something
// other than a pairing link.
var ErrNotPairing = errors.New("that QR code is not a pitwall pairing link")

// ParseScan reads a scanned QR code, which must be the link pitwall
// remote pair shows: an http or https address with a #pair= code. A
// Wi-Fi code, another site's address or plain text is ErrNotPairing.
func ParseScan(text string) (server, pairCode string, err error) {
	u, err := url.Parse(strings.TrimSpace(text))
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", "", ErrNotPairing
	}
	if frag, _ := url.ParseQuery(u.Fragment); frag.Get("pair") == "" {
		return "", "", ErrNotPairing
	}
	return ParseInvite(text, "")
}

// SamePin reports whether two SHA-256 fingerprints match, whatever their
// case, colons or spaces.
func SamePin(a, b string) bool {
	norm := func(s string) string {
		return strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(s))
	}
	return a != "" && norm(a) == norm(b)
}
