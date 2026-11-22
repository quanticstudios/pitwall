package tsnet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	tsnetlib "tailscale.com/tsnet"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
	"tailscale.com/types/views"

	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestPolicyCheckFromFakeWhoIs(t *testing.T) {
	alice := PeerFromWhoIs(&apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "node-alice", ComputedName: "iphone"},
		UserProfile: &tailcfg.UserProfile{LoginName: "Alice@Example.com", DisplayName: "Alice"},
	})
	bob := PeerFromWhoIs(&apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "node-bob", ComputedName: "pixel"},
		UserProfile: &tailcfg.UserProfile{LoginName: "bob@example.com", DisplayName: "Bob"},
	})
	taggedPhone := PeerFromWhoIs(&apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "node-tag1", ComputedName: "work-phone", Tags: []string{"tag:phones"}},
		UserProfile: &tailcfg.UserProfile{LoginName: "tagged-devices"},
	})
	taggedOther := PeerFromWhoIs(&apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "node-tag2", ComputedName: "ci", Tags: []string{"tag:ci"}},
		UserProfile: &tailcfg.UserProfile{LoginName: "tagged-devices"},
	})

	if alice.DeviceName() != "alice@example.com (iphone)" {
		t.Errorf("alice DeviceName = %q", alice.DeviceName())
	}
	if taggedPhone.DeviceName() != "work-phone (tag:phones)" {
		t.Errorf("taggedPhone DeviceName = %q", taggedPhone.DeviceName())
	}
	if !strings.HasPrefix(alice.DeviceID(), "ts-") || alice.DeviceID() == bob.DeviceID() {
		t.Errorf("DeviceIDs: alice=%q bob=%q", alice.DeviceID(), bob.DeviceID())
	}
	if taggedPhone.LoginName != "" || !taggedPhone.IsTagged {
		t.Errorf("tagged peer leaked login name: %+v", taggedPhone)
	}

	// Default policy: only the tailnet user who logged pitwall in.
	pol := Policy{SelfUser: "alice@example.com"}
	if ok, reason := pol.Check(alice); !ok {
		t.Errorf("self user denied: %s", reason)
	}
	if ok, reason := pol.Check(bob); ok || !strings.Contains(reason, "allow_users") {
		t.Errorf("other user: ok=%v reason=%q", ok, reason)
	}
	if ok, reason := pol.Check(taggedPhone); ok || !strings.Contains(reason, "allow_tags") {
		t.Errorf("tagged node with empty allow_tags: ok=%v reason=%q", ok, reason)
	}
	if ok, _ := pol.Check(PeerFromWhoIs(nil)); ok {
		t.Error("nil WhoIs allowed")
	}

	// Extra allowed users and tags.
	pol.AllowUsers = []string{"Bob@Example.com", "tagged-devices"}
	pol.AllowTags = []string{"tag:phones"}
	if ok, reason := pol.Check(bob); !ok {
		t.Errorf("allowed user denied: %s", reason)
	}
	if ok, reason := pol.Check(taggedPhone); !ok {
		t.Errorf("allowed tag denied: %s", reason)
	}
	// A tagged node whose UserProfile.LoginName is "tagged-devices" must NOT match AllowUsers.
	if ok, _ := pol.Check(taggedOther); ok {
		t.Error("disallowed tagged node matched AllowUsers")
	}

	// Tagged pitwall node: SelfUserFromStatus returns "" so nobody gets in by default.
	tags := views.SliceOf([]string{"tag:server"})
	taggedSelf := SelfUserFromStatus(&ipnstate.Status{
		Self: &ipnstate.PeerStatus{UserID: 1, Tags: &tags},
		User: map[tailcfg.UserID]tailcfg.UserProfile{1: {LoginName: "tagged-devices"}},
	})
	if taggedSelf != "" {
		t.Errorf("SelfUserFromStatus for tagged node = %q, want empty", taggedSelf)
	}
	userSelf := SelfUserFromStatus(&ipnstate.Status{
		Self: &ipnstate.PeerStatus{UserID: 2},
		User: map[tailcfg.UserID]tailcfg.UserProfile{2: {LoginName: "Alice@Example.com"}},
	})
	if userSelf != "alice@example.com" {
		t.Errorf("SelfUserFromStatus for user node = %q", userSelf)
	}
	if ok, reason := (Policy{SelfUser: taggedSelf}).Check(alice); ok || !strings.Contains(reason, "no owner user") {
		t.Errorf("tagged pitwall node without allow_users: ok=%v reason=%q", ok, reason)
	}
}

func TestResolveAuthKeyPrecedence(t *testing.T) {
	t.Setenv("PITWALL_TAILSCALE_AUTHKEY", "key-from-pitwall")
	t.Setenv("TS_AUTHKEY", "key-from-ts")
	if got := ResolveAuthKey("key-from-config"); got != "key-from-config" {
		t.Errorf("config precedence: %q", got)
	}
	if got := ResolveAuthKey(""); got != "key-from-pitwall" {
		t.Errorf("PITWALL_TAILSCALE_AUTHKEY precedence: %q", got)
	}
	t.Setenv("PITWALL_TAILSCALE_AUTHKEY", "")
	if got := ResolveAuthKey(""); got != "key-from-ts" {
		t.Errorf("TS_AUTHKEY fallback: %q", got)
	}
}

func startTestControl(t *testing.T) (string, *testcontrol.Server) {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	control := &testcontrol.Server{
		DERPMap: derpMap,
		DNSConfig: &tailcfg.DNSConfig{
			Proxied: true,
		},
		MagicDNSDomain: "tail-test.ts.net",
		Logf:           func(string, ...any) {},
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control.HTTPTestServer.URL, control
}

func selfSignedTLS(t *testing.T, dnsName string) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{dnsName},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}, pool
}

// TestNodeStartServeWhoIsAndStop runs a real in-process tsnet.Node against
// tailscale's local testcontrol server with a temp state dir (never touching
// any real tailnet or system tailscaled), verifying HTTPS serving, WhoIs
// authorization, policy updates, and clean Stop + Logout.
func TestNodeStartServeWhoIsAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping tsnet testcontrol integration test in -short mode")
	}
	controlURL, _ := startTestControl(t)
	remoteDir := t.TempDir()
	stateDir := filepath.Join(remoteDir, "tsnet")
	dnsName := "pitwall-test.tail-test.ts.net"
	srvTLS, pool := selfSignedTLS(t, dnsName)

	var (
		mu       sync.Mutex
		statuses []remote.TailscaleStatus
		running  = make(chan remote.TailscaleStatus, 1)
	)
	node, err := Start(Config{
		Dir:        stateDir,
		Hostname:   "pitwall-test",
		ControlURL: controlURL,
		TLSConfig:  srvTLS,
		OnStatus: func(st remote.TailscaleStatus) {
			mu.Lock()
			statuses = append(statuses, st)
			mu.Unlock()
			if st.State == remote.TailscaleRunning {
				select {
				case running <- st:
				default:
				}
			}
		},
	}, func(authorize func(*http.Request) (remote.Device, int, string), deviceLive func(remote.Device) bool) http.Handler {
		rs := &remote.Server{
			Dir:        remoteDir,
			Items:      func() []remote.Item { return []remote.Item{{Pane: "p1", Detail: "Approve deploy"}} },
			Answer:     func(string, int64, bool) error { return nil },
			Reply:      func(string, int64, string) error { return nil },
			Authorize:  authorize,
			DeviceLive: deviceLive,
		}
		return rs.Handler()
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = node.Stop(false)
	}()

	var st remote.TailscaleStatus
	select {
	case st = <-running:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for tsnet Running; last status = %+v", node.Status())
	}
	if st.Hostname != "pitwall-test" || !strings.HasPrefix(st.URL, "https://") || len(st.IPs) == 0 {
		t.Fatalf("unexpected running status: %+v", st)
	}
	if st.SelfUser == "" {
		t.Fatalf("expected SelfUser to be populated from testcontrol, got %+v", st)
	}

	// Bring up a second tsnet node on the same testcontrol plane to act as the phone.
	phone := &tsnetlib.Server{
		Dir:        filepath.Join(t.TempDir(), "phone"),
		Hostname:   "test-phone",
		ControlURL: controlURL,
		Logf:       func(string, ...any) {},
		UserLogf:   func(string, ...any) {},
	}
	t.Cleanup(func() { _ = phone.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := phone.Up(ctx); err != nil {
		t.Fatalf("phone.Up: %v", err)
	}

	httpc := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: phone.Dial,
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: dnsName,
			},
		},
	}

	// testcontrol assigns each registered node a distinct user (user-1 for pitwall,
	// user-2 for test-phone), so by default test-phone is denied with 403.
	resp, err := httpc.Get("https://" + st.IPs[0] + ":443/api/items")
	if err != nil {
		t.Fatalf("GET /api/items over tsnet HTTPS: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "allow_users") {
		t.Fatalf("other user before allow_users: status=%d body=%s", resp.StatusCode, body)
	}

	// Extract test-phone's login name from the denial and grant it via live UpdatePolicy.
	phoneStatus, err := phone.Up(ctx)
	if err != nil {
		t.Fatalf("phone.Up status: %v", err)
	}
	phoneUser := SelfUserFromStatus(phoneStatus)
	if phoneUser == "" {
		t.Fatalf("could not read phone user from testcontrol status")
	}
	node.UpdatePolicy([]string{phoneUser}, nil)

	// Now test-phone is allowed with zero pairing!
	resp, err = httpc.Get("https://" + st.IPs[0] + ":443/api/items")
	if err != nil {
		t.Fatalf("GET /api/items after allow_users: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/items status=%d body=%s", resp.StatusCode, body)
	}
	var payload struct {
		Items  []remote.Item `json:"items"`
		Device string        `json:"device"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(payload.Items) != 1 || payload.Items[0].Detail != "Approve deploy" || !strings.Contains(payload.Device, phoneUser) {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	// Revoking allow_users denies test-phone again, while a paired-device Bearer token still works beside it.
	node.UpdatePolicy(nil, nil)
	resp, err = httpc.Get("https://" + st.IPs[0] + ":443/api/items")
	if err != nil {
		t.Fatalf("GET /api/items after revoke: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 after removing allow_users, got %d", resp.StatusCode)
	}

	// Pair a device token and verify it works over the tsnet HTTPS listener alongside WhoIs.
	pairCode, err := remote.Pair(remoteDir, "paired-pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pairResp, err := httpc.Post("https://"+st.IPs[0]+":443/api/pair", "application/json", strings.NewReader(`{"code":"`+pairCode+`"}`))
	if err != nil {
		t.Fatalf("POST /api/pair over tsnet: %v", err)
	}
	pairBody, _ := io.ReadAll(pairResp.Body)
	pairResp.Body.Close()
	var pr struct{ Token string }
	if err := json.Unmarshal(pairBody, &pr); err != nil || pairResp.StatusCode != http.StatusOK || pr.Token == "" {
		t.Fatalf("pair over tsnet: status=%d body=%s", pairResp.StatusCode, pairBody)
	}
	req, _ := http.NewRequest("GET", "https://"+st.IPs[0]+":443/api/items", nil)
	req.Header.Set("Authorization", "Bearer "+pr.Token)
	resp, err = httpc.Do(req)
	if err != nil {
		t.Fatalf("GET /api/items with paired token: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"device":"paired-pixel"`) {
		t.Fatalf("paired token over tsnet: status=%d body=%s", resp.StatusCode, body)
	}

	// Stop with logout=true cleanly removes the node and reports off.
	if err := node.Stop(true); err != nil {
		t.Fatalf("Stop(logout=true): %v", err)
	}
	if got := node.Status().State; got != remote.TailscaleOff {
		t.Fatalf("status after stop = %q, want off", got)
	}
}
