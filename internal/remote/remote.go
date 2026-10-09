// Package remote is the phone page: pairing, device tokens, and the HTTP
// server a paired phone uses to see which agents need you and answer them.
// The daemon serves it and supplies the state and the keys.
//
// Everything lives in Dir, mode 0700: pair.json holds the pending pairing
// code's hash, devices/<id>.json each paired device's token hash,
// devices/<id>.push.json its push subscription, vapid.pem the key that
// signs pushes, and tls.pem the certificate for tls = true. The CLI and the settings page
// write pairings and revoke devices there; the daemon reads it on every
// request, so a revoked device is refused at once.
package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

// PairTTL is how long a pairing code works.
const PairTTL = 10 * time.Minute

// Dir is the remote folder in the state folder.
func Dir() string { return filepath.Join(config.StateDir(), "remote") }

// Device is a paired phone.
type Device struct {
	ID     string
	Name   string
	Hash   string // hex SHA-256 of the token's secret; the token is not kept
	Paired time.Time
}

// pairing is pair.json: a code not yet used.
type pairing struct {
	Hash    string // hex SHA-256 of the code
	Name    string // the device it pairs
	Expires time.Time
}

var (
	errBadCode = errors.New("this pairing code is wrong, used or expired; pair again")
	idRe       = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b) // never fails; see crypto/rand.Read
	return b
}

// writeJSON writes v to path with mode 0600, atomically.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
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

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// Pair starts pairing a device called name ("phone" when empty, numbered
// when taken) and returns the one-time code, good for PairTTL. A new
// pairing replaces one not yet used.
func Pair(dir, name string, now time.Time) (code string, err error) {
	devs, err := Devices(dir)
	if err != nil {
		return "", err
	}
	name = strings.Join(strings.Fields(strings.Map(printable, name)), " ")
	if name == "" {
		name = "phone"
	}
	taken := func(n string) bool { return slices.ContainsFunc(devs, func(d Device) bool { return d.Name == n }) }
	for i, base := 2, name; taken(name); i++ {
		name = fmt.Sprintf("%s %d", base, i)
	}
	code = base32.StdEncoding.EncodeToString(random(15))
	return code, writeJSON(filepath.Join(dir, "pair.json"), pairing{Hash: hash(code), Name: name, Expires: now.Add(PairTTL)})
}

// PairName is the name of the device the pending pairing pairs, "" when
// none is pending.
func PairName(dir string, now time.Time) string {
	var p pairing
	if readJSON(filepath.Join(dir, "pair.json"), &p) != nil || now.After(p.Expires) {
		return ""
	}
	return p.Name
}

// redeem uses code, once, and pairs its device. It returns the device's
// token, which nothing keeps but the phone. Callers serialize it.
func redeem(dir, code string, now time.Time) (string, Device, error) {
	path := filepath.Join(dir, "pair.json")
	var p pairing
	if err := readJSON(path, &p); err != nil {
		return "", Device{}, errBadCode
	}
	if subtle.ConstantTimeCompare([]byte(hash(code)), []byte(p.Hash)) != 1 {
		return "", Device{}, errBadCode
	}
	// Used or expired, the code goes; a failed remove keeps it unused.
	if err := os.Remove(path); err != nil {
		return "", Device{}, err
	}
	if now.After(p.Expires) {
		return "", Device{}, errBadCode
	}
	secret := base64.RawURLEncoding.EncodeToString(random(32))
	d := Device{ID: hex.EncodeToString(random(8)), Name: p.Name, Hash: hash(secret), Paired: now}
	if err := writeJSON(filepath.Join(dir, "devices", d.ID+".json"), d); err != nil {
		return "", Device{}, err
	}
	return d.ID + "." + secret, d, nil
}

// check returns the device token belongs to.
func check(dir, token string) (Device, bool) {
	id, secret, ok := strings.Cut(token, ".")
	if !ok || !idRe.MatchString(id) {
		return Device{}, false
	}
	var d Device
	if readJSON(filepath.Join(dir, "devices", id+".json"), &d) != nil || d.ID != id {
		return Device{}, false
	}
	return d, subtle.ConstantTimeCompare([]byte(hash(secret)), []byte(d.Hash)) == 1
}

// Devices are the paired devices, oldest first.
func Devices(dir string) ([]Device, error) {
	es, err := os.ReadDir(filepath.Join(dir, "devices"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, e := range es {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		var d Device
		if !ok || !idRe.MatchString(id) || readJSON(filepath.Join(dir, "devices", e.Name()), &d) != nil {
			continue
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Device) int { return a.Paired.Compare(b.Paired) })
	return out, nil
}

// Revoke unpairs the device whose id or name is which; its token stops
// working at once, and its push subscription goes with it.
func Revoke(dir, which string) (Device, error) {
	devs, err := Devices(dir)
	if err != nil {
		return Device{}, err
	}
	i := slices.IndexFunc(devs, func(d Device) bool { return d.ID == which || d.Name == which })
	if i < 0 {
		return Device{}, fmt.Errorf("no paired device %q; pitwall remote devices lists them", which)
	}
	if err := os.Remove(filepath.Join(dir, "devices", devs[i].ID+".json")); err != nil {
		return devs[i], err
	}
	if err := os.Remove(pushPath(dir, devs[i].ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return devs[i], err
	}
	return devs[i], nil
}

func printable(r rune) rune {
	if unicode.IsControl(r) {
		return -1
	}
	return r
}

// Clean makes a reply safe to paste into a pane: line breaks become \n
// and tabs spaces, and every other control character, Esc and the C1 set
// included, goes, so the text can neither end a bracketed paste nor press
// a key.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", " ").Replace(s)
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		return printable(r)
	}, s))
}

// answers are the keys each agent's permission prompt takes: Allow picks
// its first option, "Yes", and Deny is Esc, which both read as no. Claude
// Code picks an option by its number; Codex by the letter after it, "Yes,
// proceed (y)", since its numbers are not shortcuts.
var answers = map[model.Provider]struct{ allow, deny key.Name }{
	model.ProviderClaude: {"1", key.NameEscape},
	model.ProviderCodex:  {"Y", key.NameEscape},
}

// Answerable reports whether a is a permission prompt that Allow and Deny
// answer: pending approval, from an agent whose prompts pitwall knows.
func Answerable(a model.Activity) bool {
	_, known := answers[a.Provider]
	return a.State == model.StatePendingApproval && known
}

// AnswerKey is the key that answers p's permission prompt, and whether
// pitwall knows its prompts.
func AnswerKey(p model.Provider, allow bool) (key.Name, bool) {
	a, ok := answers[p]
	if allow {
		return a.allow, ok
	}
	return a.deny, ok
}
