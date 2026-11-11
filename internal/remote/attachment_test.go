package remote

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestUpload(t *testing.T) {
	s, _ := newServer(t)
	s.Tabs = func() []Tab { return nil }
	calls := 0
	s.Upload = func(pane string, b []byte) (string, error) {
		calls++
		if pane != "p" || string(b) != "picture" {
			return "", errors.New("bad image")
		}
		return "image-test.png", nil
	}
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	for _, tc := range []struct {
		token, body string
		code        int
	}{{"", "picture", 401}, {token, "picture", 200}, {token, "wrong", 400}, {token, string(bytes.Repeat([]byte("x"), proto.MaxPasteImage+1)), 413}} {
		if code, _ := call(t, h, "POST", "/api/upload?pane=p", tc.token, tc.body); code != tc.code {
			t.Errorf("upload got %d want %d", code, tc.code)
		}
	}
	if calls != 2 {
		t.Fatalf("upload callback called %d times", calls)
	}
	r := httptest.NewRequest("POST", "/api/upload?pane=p", nil)
	r.Header.Set("Origin", "https://other.example.com")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("accepted cross-origin upload")
	}
}
