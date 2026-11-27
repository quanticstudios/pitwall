package phone

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A route an older daemon lacks is ErrNotServed; a handler's own 404 keeps
// its words.
func TestNotServed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/file", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such file", http.StatusNotFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := NewClient(Creds{Server: ts.URL + "/", Token: "t"})
	if err := c.Restart(context.Background(), "p1"); !errors.Is(err, ErrNotServed) {
		t.Fatalf("Restart on an old daemon = %v", err)
	}
	if _, err := c.File(context.Background(), "w1", "a.md"); err == nil || err.Error() != "no such file" {
		t.Fatalf("File = %v", err)
	}
	// A wrong address is not an old daemon.
	if _, err := Pair(context.Background(), ts.URL+"/", "ABC", ""); err == nil || !strings.Contains(err.Error(), "no pitwall phone page") {
		t.Fatalf("Pair at a wrong address = %v", err)
	}
}
