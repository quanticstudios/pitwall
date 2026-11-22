package daemon

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/web"
)

// TestServeRemoteWebCapabilities verifies that serveRemote wires Chats and
// Usage into its web.Server so the phone's "Open the full client" /ws
// advertises chat, forge and usage capabilities and accepts commands.
func TestServeRemoteWebCapabilities(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	remoteDir := t.TempDir()
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		o: Options{
			RemoteDir: remoteDir,
			Usage:     func(time.Time) web.Usage { return web.Usage{} },
		},
	}
	stop := d.serveRemote(phone{
		RemoteSettings: config.RemoteSettings{Enabled: true, Listen: addr},
		Bundle:         bundle,
	})
	if stop == nil {
		t.Fatal("serveRemote returned nil")
	}
	defer stop()

	base := "http://" + addr
	code, err := remote.Pair(remoteDir, "phone", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	resp, err := hc.Post(base+"/api/pair", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var paired struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&paired); err != nil || resp.StatusCode != 200 || paired.Token == "" {
		resp.Body.Close()
		t.Fatalf("pair: %d %+v %v", resp.StatusCode, paired, err)
	}
	resp.Body.Close()

	req, _ := http.NewRequest("POST", base+"/api/web", nil)
	req.Header.Set("Authorization", "Bearer "+paired.Token)
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST /api/web: %d", resp.StatusCode)
	}

	resp, err = hc.Get(base + "/_pitwall/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	var boot struct{ URL, Token string }
	if err := json.NewDecoder(resp.Body).Decode(&boot); err != nil || resp.StatusCode != 200 || boot.Token == "" {
		resp.Body.Close()
		t.Fatalf("bootstrap: %d %+v %v", resp.StatusCode, boot, err)
	}
	resp.Body.Close()

	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(5 * time.Second))

	upgradeReq := "GET /ws HTTP/1.1\r\nHost: " + addr + "\r\nOrigin: " + base + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(nc, upgradeReq); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(nc)
	upResp, err := http.ReadResponse(br, nil)
	if err != nil || upResp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", upResp, err)
	}

	writeWS := func(v any) {
		t.Helper()
		p, _ := json.Marshal(v)
		b := []byte{0x81}
		switch {
		case len(p) < 126:
			b = append(b, 0x80|byte(len(p)))
		case len(p) <= 0xffff:
			b = binary.BigEndian.AppendUint16(append(b, 0x80|126), uint16(len(p)))
		default:
			b = binary.BigEndian.AppendUint64(append(b, 0x80|127), uint64(len(p)))
		}
		mask := []byte{1, 2, 3, 4}
		b = append(b, mask...)
		for i, x := range p {
			b = append(b, x^mask[i%4])
		}
		if _, err := nc.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	readWS := func() map[string]any {
		t.Helper()
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			t.Fatal(err)
		}
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(br, b[:]); err != nil {
				t.Fatal(err)
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(br, b[:]); err != nil {
				t.Fatal(err)
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(br, p); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	writeWS(map[string]any{
		"type":     "hello",
		"protocol": web.Protocol,
		"token":    boot.Token,
		"client":   map[string]string{"name": "phone-web", "version": "1"},
	})
	welcome := readWS()
	if welcome["type"] != "welcome" {
		t.Fatalf("got %v, want welcome", welcome)
	}
	rawCaps, _ := welcome["capabilities"].([]any)
	caps := make([]string, 0, len(rawCaps))
	for _, c := range rawCaps {
		if s, ok := c.(string); ok {
			caps = append(caps, s)
		}
	}
	for _, want := range []string{"subscribeSessions", "subscribe", "listSessions", "readUsage"} {
		if !slices.Contains(caps, want) {
			t.Fatalf("capabilities %v missing %q", caps, want)
		}
	}

	writeWS(map[string]any{"type": "listSessions", "id": "1"})
	res := readWS()
	if res["type"] != "result" || res["id"] != "1" || res["ok"] != true {
		t.Fatalf("listSessions returned %v, want ok=true", res)
	}
}
