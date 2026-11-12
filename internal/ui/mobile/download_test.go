package mobile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAPKDownloadHosts(t *testing.T) {
	for _, c := range []struct {
		url  string
		want bool
	}{
		{"https://github.com/quanticstudios/pitwall/releases/download/v0.3.0/pitwall-phone.apk", true},
		{"https://release-assets.githubusercontent.com/file", true},
		{"https://objects.githubusercontent.com/file", true},
		{"http://github.com/file", false},
		{"https://github.com.example.com/file", false},
		{"https://example.com/file", false},
		{"https://user@github.com/file", false},
		{"https://github.com:8443/file", false},
		{"https://github.com/file#fragment", false},
		{"file:///app.apk", false},
		{"http://127.0.0.1:8089/apk", false},
	} {
		if got := validAPKURL(c.url, ""); got != c.want {
			t.Errorf("validAPKURL(%q)=%v", c.url, got)
		}
	}
	if !validAPKURL("http://127.0.0.1:8089/apk", "http://127.0.0.1:8089") {
		t.Fatal("compiled loopback fixture refused")
	}
	if validAPKURL("http://example.com/apk", "http://example.com") {
		t.Fatal("non-loopback fixture accepted")
	}
}

func TestDownloadAPK(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/apk", http.StatusFound)
		case "/outside":
			http.Redirect(w, r, "https://example.com/app.apk", http.StatusFound)
		case "/apk":
			fmt.Fprint(w, "bytes")
		case "/short":
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "four")
		case "/long":
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "sixsix")
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	for _, c := range []struct {
		endpoint string
		size     int64
		want     bool
	}{
		{"/apk", 5, true}, {"/redirect", 5, true}, {"/apk", 4, false},
		{"/short", 5, false}, {"/long", 5, false}, {"/outside", 5, false},
		{"/forbidden", 5, false}, {"/apk", 0, false}, {"/apk", maxAPKSize + 1, false},
	} {
		t.Run(c.endpoint+fmt.Sprint(c.size), func(t *testing.T) {
			dir := t.TempDir()
			var progress int64
			path, err := downloadAPK(context.Background(), srv.Client(), srv.URL+c.endpoint, c.size, dir, srv.URL, func(n int64) { progress = n })
			if (err == nil) != c.want {
				t.Fatalf("download=%q,%v", path, err)
			}
			if c.want {
				b, err := os.ReadFile(path)
				if err != nil || string(b) != "bytes" || progress != c.size {
					t.Fatalf("file=%q,progress=%d,error=%v", b, progress, err)
				}
				os.Remove(path)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("cache has files: %v, %v", files, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := downloadAPK(ctx, srv.Client(), srv.URL+"/apk", 5, t.TempDir(), srv.URL, nil); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if _, err := downloadAPK(context.Background(), srv.Client(), srv.URL+"/apk", 5, t.TempDir(), "", nil); err == nil {
		t.Fatal("accepted a non-GitHub download without fixture override")
	}
}
