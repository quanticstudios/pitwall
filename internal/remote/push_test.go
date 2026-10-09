package remote

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := unb64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEncryptRFC8291 is the worked example of RFC 8291, Appendix A.
func TestEncryptRFC8291(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	ua, err := ecdh.P256().NewPublicKey(mustB64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"), ua,
		mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg"), as, mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("got  %s\nwant %s", b64.EncodeToString(got), want)
	}
	// The test's decrypt, which the round trip relies on, opens it too.
	uaKey, err := ecdh.P256().NewPrivateKey(mustB64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := decrypt(got, uaKey, mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg")); string(plain) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("decrypt %q, %v", plain, err)
	}
}

// decrypt is the browser's side of encrypt, for ua and auth.
func decrypt(msg []byte, ua *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(msg) < 86 || binary.BigEndian.Uint32(msg[16:20]) != recordSize || msg[20] != 65 {
		return nil, fmt.Errorf("header % x", msg[:min(len(msg), 21)])
	}
	salt, asPub, body := msg[:16], msg[21:86], msg[86:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		return nil, err
	}
	secret, _ := ua.ECDH(as)
	prkKey, _ := hkdf.Extract(sha256.New, secret, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, fmt.Errorf("open: %v %q", err, plain)
	}
	return plain[:len(plain)-1], nil
}

// checkJWT checks an RFC 8292 token: ES256 by pub over endpoint's origin.
func checkJWT(t *testing.T, jwt string, pub *ecdsa.PublicKey, endpoint string, now time.Time) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	var head, claims map[string]any
	json.Unmarshal(mustB64(t, parts[0]), &head)
	json.Unmarshal(mustB64(t, parts[1]), &claims)
	if head["alg"] != "ES256" || head["typ"] != "JWT" {
		t.Errorf("header %v", head)
	}
	aud := endpoint[:strings.Index(endpoint[len("https://"):], "/")+len("https://")]
	exp, _ := claims["exp"].(float64)
	if claims["aud"] != aud || !strings.HasPrefix(claims["sub"].(string), "https://") ||
		exp <= float64(now.Unix()) || exp > float64(now.Add(24*time.Hour).Unix()) {
		t.Errorf("claims %v, want aud %s", claims, aud)
	}
	sig := mustB64(t, parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatalf("signature does not verify (%d bytes)", len(sig))
	}
}

func TestVAPIDJWT(t *testing.T) {
	dir := t.TempDir()
	k, err := VAPIDKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := VAPIDKey(dir); err != nil || !again.Equal(k) {
		t.Fatalf("second load %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "vapid.pem")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("vapid.pem mode %v", fi.Mode().Perm())
	}
	if pub := mustB64(t, VAPIDPublic(k)); len(pub) != 65 || pub[0] != 4 {
		t.Errorf("public key % x", pub)
	}
	now := time.Now()
	jwt, err := vapidJWT(k, "https://push.example.net:8443/send/abc?x=1", now.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	checkJWT(t, jwt, &k.PublicKey, "https://push.example.net:8443/send/abc?x=1", now)
	// A JWT signed by another key does not verify with this one.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), nil)
	forged, _ := vapidJWT(other, "https://push.example.net/x", now.Add(time.Hour))
	parts := strings.Split(forged, ".")
	sig := mustB64(t, parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if ecdsa.Verify(&k.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("another key's signature verified")
	}
}

// browser is a phone's push subscription with its private key, and the
// push service it points at.
type browser struct {
	key   *ecdh.PrivateKey
	auth  []byte
	srv   *httptest.Server
	got   chan []byte // decrypted messages
	authz chan string
	code  int // what the push service answers
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	b := &browser{auth: random(16), got: make(chan []byte, 10), authz: make(chan string, 10), code: http.StatusCreated}
	b.key, _ = ecdh.P256().GenerateKey(nil)
	b.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" || r.Header.Get("Urgency") == "" {
			t.Errorf("headers %v", r.Header)
		}
		b.authz <- r.Header.Get("Authorization")
		plain, err := decrypt(body, b.key, b.auth)
		if err != nil {
			t.Errorf("push body: %v", err) // and the receiver reads nil
		}
		b.got <- plain
		w.WriteHeader(b.code)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *browser) sub() Subscription {
	return Subscription{Endpoint: b.srv.URL + "/push/abc", Keys: PushKeys{P256dh: b64.EncodeToString(b.key.PublicKey().Bytes()), Auth: b64.EncodeToString(b.auth)}}
}

// TestPushRoundTrip: a subscribed device gets the notice, encrypted for
// it and signed with the VAPID key; a revoked one gets nothing.
func TestPushRoundTrip(t *testing.T) {
	s, h := newServer(t)
	token := pairDevice(t, s, h, "pixel")
	pairDevice(t, s, h, "quiet") // never subscribes
	b := newBrowser(t)
	sub, _ := json.Marshal(b.sub())
	if st, body := call(t, h, "POST", "/api/push", token, string(sub)); st != 200 {
		t.Fatalf("subscribe: %d %s", st, body)
	}
	st, body := call(t, h, "GET", "/api/push", token, "")
	var r struct {
		Key        string
		Subscribed bool
	}
	json.Unmarshal([]byte(body), &r)
	key, _ := VAPIDKey(s.Dir)
	if st != 200 || !r.Subscribed || r.Key != VAPIDPublic(key) {
		t.Fatalf("push state: %d %s", st, body)
	}
	p := &Pusher{Dir: s.Dir, Client: b.srv.Client()}
	n := Notice{Title: "fix auth", State: "pending-approval", Body: "Approval: Bash", Pane: "p1", Urgent: true}
	p.Send(context.Background(), n)
	var got Notice
	if err := json.Unmarshal(<-b.got, &got); err != nil || got.Title != n.Title || got.Body != n.Body || got.Pane != "p1" || got.State != n.State {
		t.Fatalf("got %+v, %v", got, err)
	}
	authz := <-b.authz
	jwt, k, ok := strings.Cut(strings.TrimPrefix(authz, "vapid t="), ", k=")
	if !ok || k != VAPIDPublic(key) {
		t.Fatalf("Authorization %q", authz)
	}
	checkJWT(t, jwt, &key.PublicKey, b.srv.URL+"/push/abc", time.Now())
	if len(b.got) != 0 {
		t.Fatal("the device without a subscription got a push")
	}

	if _, err := Revoke(s.Dir, "pixel"); err != nil {
		t.Fatal(err)
	}
	devs, _ := Devices(s.Dir)
	if len(devs) != 1 || Subscribed(s.Dir, devs[0].ID) {
		t.Fatalf("after revoke: %+v", devs)
	}
	if es, _ := os.ReadDir(filepath.Join(s.Dir, "devices")); len(es) != 1 {
		t.Fatalf("revoke left %d files", len(es))
	}
	p.Send(context.Background(), n)
	if len(b.got) != 0 {
		t.Fatal("a revoked device got a push")
	}
}

func TestSubscribeChecks(t *testing.T) {
	s, h := newServer(t)
	token := pairDevice(t, s, h, "")
	good := newBrowser(t).sub()
	for name, sub := range map[string]Subscription{
		"http endpoint": {Endpoint: "http://push.example.net/x", Keys: good.Keys},
		"no key":        {Endpoint: good.Endpoint, Keys: PushKeys{Auth: good.Keys.Auth}},
		"short auth":    {Endpoint: good.Endpoint, Keys: PushKeys{P256dh: good.Keys.P256dh, Auth: "AAAA"}},
	} {
		body, _ := json.Marshal(sub)
		if st, _ := call(t, h, "POST", "/api/push", token, string(body)); st != 400 {
			t.Errorf("%s: %d", name, st)
		}
	}
	body, _ := json.Marshal(good)
	if st, _ := call(t, h, "POST", "/api/push", "", string(body)); st != 401 {
		t.Errorf("no token: %d", st)
	}
	if es, _ := os.ReadDir(filepath.Join(s.Dir, "devices")); len(es) != 1 {
		t.Fatalf("%d files after refused subscriptions", len(es))
	}
}

// TestPushGoneAndLimited: a push service's 410 drops the subscription,
// and a device gets at most pushBurst pushes a pushWindow.
func TestPushGoneAndLimited(t *testing.T) {
	s, h := newServer(t)
	now := time.Now()
	pairDevice(t, s, h, "pixel")
	b := newBrowser(t)
	devs, _ := Devices(s.Dir)
	id := devs[0].ID
	if err := Subscribe(s.Dir, id, b.sub()); err != nil {
		t.Fatal(err)
	}
	p := &Pusher{Dir: s.Dir, Client: b.srv.Client(), now: func() time.Time { return now }}
	for range pushBurst + 2 {
		p.Send(context.Background(), Notice{Title: "t"})
	}
	if len(b.got) != pushBurst {
		t.Fatalf("%d pushes, want %d", len(b.got), pushBurst)
	}
	for len(b.got) > 0 {
		<-b.got
		<-b.authz
	}
	now = now.Add(pushWindow)
	b.code = http.StatusGone
	p.Send(context.Background(), Notice{Title: "t"})
	<-b.got
	if Subscribed(s.Dir, id) {
		t.Fatal("a 410 kept the subscription")
	}
	if err := p.Test(context.Background(), id); err == nil || errors.Is(err, errGone) {
		t.Fatalf("test push without a subscription: %v", err)
	}
}

func TestNoticeOf(t *testing.T) {
	st := model.State{Workspaces: []model.Workspace{{ID: "w", Label: "fix auth"}}}
	a := model.Activity{PaneID: "p", WorkspaceID: "w", State: model.StatePendingApproval, Detail: strings.Repeat("x", 200)}
	n := NoticeOf(st, a)
	if n.Title != "fix auth" || n.Pane != "p" || !n.Urgent || n.Body != "Approval: "+strings.Repeat("x", 120) {
		t.Fatalf("%+v", n)
	}
	a.State, a.Detail, a.WorkspaceID = model.StateCompleted, "", "gone"
	if n := NoticeOf(st, a); n.Title != "pitwall" || n.Urgent || n.Body != "Done" {
		t.Fatalf("%+v", n)
	}
}
