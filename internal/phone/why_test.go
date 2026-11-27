package phone

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A refused, unknown or silent address reads as words, not Go's dial error.
func TestWhy(t *testing.T) {
	get := func(u string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		_, err := http.DefaultClient.Do(r)
		return err
	}
	if w := Why(get("http://127.0.0.1:1/api/stream")); !strings.HasPrefix(w, "nothing answers there") {
		t.Errorf("refused: %q", w)
	}
	if w := Why(get("http://no-such-host.invalid/")); !strings.HasPrefix(w, "cannot find no-such-host.invalid") {
		t.Errorf("unknown host: %q", w)
	}
	if w := Why(errors.New("no chat p1")); w != "no chat p1" {
		t.Errorf("plain: %q", w)
	}
	if Why(nil) != "" {
		t.Error("nil")
	}
}
