package remote

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Web Push: delivery is RFC 8030, the message encryption RFC 8291 and
// the sender's identity RFC 8292 (VAPID). A push service (FCM, Mozilla's,
// Apple's) carries the message; it sees the endpoint and the size, never
// the text.

// Subscription is a browser's push subscription, as
// PushSubscription.toJSON gives it: where to send, and the browser's
// keys, base64url.
type Subscription struct {
	Endpoint string   `json:"endpoint"`
	Keys     PushKeys `json:"keys"`
}

// PushKeys are a subscription's P-256 public key and auth secret.
type PushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// Notice is what one push shows: the tab, its state and a short line of
// what it asks, as the page shows it; never the pane's screen.
type Notice struct {
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	Pane   string `json:"pane"` // the page opens on it
	Urgent bool   `json:"-"`
}

const (
	// recordSize is the one aes128gcm record a message is.
	recordSize = 4096
	// pushTTL is how long a push service keeps a message for a phone
	// that is off.
	pushTTL = 4 * time.Hour
	// vapidSubject is the contact RFC 8292 asks for; Apple refuses a
	// push without one.
	vapidSubject = "https://github.com/quanticstudios/pitwall"
	// pushBurst pushes a device gets in pushWindow; more are dropped,
	// and the page still lists them.
	pushBurst  = 6
	pushWindow = time.Minute
)

var b64 = base64.RawURLEncoding

// unb64 decodes base64url with or without padding, as browsers differ.
func unb64(s string) ([]byte, error) { return b64.DecodeString(strings.TrimRight(s, "=")) }

func pushPath(dir, id string) string { return filepath.Join(dir, "devices", id+".push.json") }

// keys checks s and returns the browser's public key and auth secret.
func (s Subscription) keys() (*ecdh.PublicKey, []byte, error) {
	if u, err := url.Parse(s.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, nil, errors.New("the push endpoint is not an https URL")
	}
	raw, err := unb64(s.Keys.P256dh)
	if err != nil {
		return nil, nil, errors.New("bad p256dh key")
	}
	ua, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, nil, errors.New("bad p256dh key")
	}
	auth, err := unb64(s.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return nil, nil, errors.New("bad auth secret")
	}
	return ua, auth, nil
}

// Subscribe keeps sub as device id's push subscription, next to its
// token, in place of the one before.
func Subscribe(dir, id string, sub Subscription) error {
	if _, _, err := sub.keys(); err != nil {
		return err
	}
	return writeJSON(pushPath(dir, id), sub)
}

// Subscribed reports whether device id has a push subscription.
func Subscribed(dir, id string) bool {
	_, err := os.Stat(pushPath(dir, id))
	return err == nil
}

// VAPIDKey is the key in dir/vapid.pem that signs every push, made on
// first use. Push services tie a subscription to its public half.
func VAPIDKey(dir string) (*ecdsa.PrivateKey, error) {
	path := filepath.Join(dir, "vapid.pem")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		k, gerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		der, gerr := x509.MarshalPKCS8PrivateKey(k)
		if gerr != nil {
			return nil, gerr
		}
		// why: the GUI's Send test and the daemon may both make one; the first link wins.
		if err = writeOnce(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err == nil || errors.Is(err, os.ErrExist) {
			data, err = os.ReadFile(path)
		}
	}
	if err != nil {
		return nil, err
	}
	if b, _ := pem.Decode(data); b != nil {
		if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
			if k, ok := k.(*ecdsa.PrivateKey); ok && k.Curve == elliptic.P256() {
				return k, nil
			}
		}
	}
	return nil, fmt.Errorf("%s: not a P-256 private key", path)
}

// VAPIDPublic is k's public key as the page's applicationServerKey: an
// uncompressed point, base64url.
func VAPIDPublic(k *ecdsa.PrivateKey) string {
	b, _ := k.PublicKey.Bytes() // a P-256 key always encodes
	return b64.EncodeToString(b)
}

// vapidJWT is the RFC 8292 token for a push to endpoint: ES256 over the
// endpoint's origin, good until exp.
func vapidJWT(k *ecdsa.PrivateKey, endpoint string, exp time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": exp.Unix(), "sub": vapidSubject})
	if err != nil {
		return "", err
	}
	in := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(in))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64) // JWS wants r and s as 32 bytes each, not ASN.1
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + b64.EncodeToString(sig), nil
}

// encrypt is RFC 8291: plain for the browser key ua and auth secret, from
// the one-time key as, as a single aes128gcm record (RFC 8188) with salt.
func encrypt(plain []byte, ua *ecdh.PublicKey, auth []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plain)+1+16 > recordSize {
		return nil, errors.New("push message too long")
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, secret, auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.Bytes())+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	head := append(append(binary.BigEndian.AppendUint32(append([]byte{}, salt...), recordSize), byte(len(asPub))), asPub...)
	return gcm.Seal(head, nonce, append(plain, 2), nil), nil // 2: the last record, unpadded
}

// errGone is a subscription its push service no longer knows.
var errGone = errors.New("the push service dropped this subscription")

// push sends n to sub, signed with key.
func push(ctx context.Context, c *http.Client, key *ecdsa.PrivateKey, sub Subscription, n Notice, now time.Time) error {
	ua, auth, err := sub.keys()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(n)
	if err != nil {
		return err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	body, err := encrypt(plain, ua, auth, as, random(16))
	if err != nil {
		return err
	}
	jwt, err := vapidJWT(key, sub.Endpoint, now.Add(12*time.Hour))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	h := req.Header
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Encoding", "aes128gcm")
	h.Set("TTL", strconv.Itoa(int(pushTTL.Seconds())))
	h.Set("Urgency", "normal")
	if n.Urgent {
		h.Set("Urgency", "high")
	}
	h.Set("Authorization", "vapid t="+jwt+", k="+VAPIDPublic(key))
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errGone
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("push service: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Pusher sends notices to the paired devices that subscribed to push.
type Pusher struct {
	Dir    string
	Client *http.Client // nil is one with a 10 s timeout

	mu   sync.Mutex
	sent map[string][]time.Time // per device, its pushes in the last pushWindow
	now  func() time.Time
}

func (p *Pusher) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (p *Pusher) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// allow counts a push to device id and reports whether it is within
// pushBurst a pushWindow.
func (p *Pusher) allow(id string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sent == nil {
		p.sent = map[string][]time.Time{}
	}
	s := p.sent[id]
	for len(s) > 0 && now.Sub(s[0]) >= pushWindow {
		s = s[1:]
	}
	ok := len(s) < pushBurst
	if ok {
		s = append(s, now)
	}
	p.sent[id] = s
	return ok
}

// Send pushes n to every subscribed device, within each one's rate, and
// drops a subscription its push service no longer knows. It reads the
// devices on every call, so a revoked one gets nothing.
func (p *Pusher) Send(ctx context.Context, n Notice) {
	devs, err := Devices(p.Dir)
	if err != nil {
		log.Printf("remote: push: %q", err)
		return
	}
	now := p.clock()
	for _, d := range devs {
		if !Subscribed(p.Dir, d.ID) {
			continue
		}
		if !p.allow(d.ID, now) {
			log.Printf("remote: push to %q dropped: more than %d a minute", d.Name, pushBurst)
			continue
		}
		if err := p.send(ctx, d, n, now); err != nil {
			log.Printf("remote: push to %q: %q", d.Name, err)
		}
	}
}

// Test pushes a test notice to device id now, outside its rate.
func (p *Pusher) Test(ctx context.Context, id string) error {
	devs, err := Devices(p.Dir)
	if err != nil {
		return err
	}
	for _, d := range devs {
		if d.ID == id {
			return p.send(ctx, d, Notice{Title: "pitwall", State: "test", Body: "Push notifications reach " + d.Name + "."}, p.clock())
		}
	}
	return fmt.Errorf("no paired device %q", id)
}

func (p *Pusher) send(ctx context.Context, d Device, n Notice, now time.Time) error {
	var sub Subscription
	if err := readJSON(pushPath(p.Dir, d.ID), &sub); err != nil {
		return errors.New(d.Name + " has not turned on notifications")
	}
	key, err := VAPIDKey(p.Dir)
	if err != nil {
		return err
	}
	err = push(ctx, p.client(), key, sub, n, now)
	if errors.Is(err, errGone) {
		os.Remove(pushPath(p.Dir, d.ID))
		log.Printf("remote: dropped %q's push subscription: its push service no longer knows it", d.Name)
	}
	return err
}

// NoticeOf is the push for activity a of st, as the page lists it.
func NoticeOf(st model.State, a model.Activity) Notice {
	n := Notice{Title: "pitwall", State: string(a.State), Body: model.PillLabel(a), Pane: a.PaneID, Urgent: model.Urgent(a)}
	for _, w := range st.Workspaces {
		if w.ID == a.WorkspaceID && w.Label != "" {
			n.Title = w.Label
		}
	}
	if d := []rune(a.Detail); len(d) > 0 {
		n.Body += ": " + string(d[:min(120, len(d))])
		if a.Provider == model.ProviderTerminal { // a terminal's notification is its own message
			n.Body = string(d[:min(120, len(d))])
		}
	}
	return n
}
