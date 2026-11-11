package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
)

func TestQuestionPageRefreshWithoutActivity(t *testing.T) {
	s, _ := newServer(t)
	var page atomic.Int32
	page.Store(1)
	s.Tabs = func() []Tab {
		return []Tab{{Item: Item{Pane: "p", Question: &agent.Question{ID: fmt.Sprint(page.Load())}}}}
	}
	unchanged := make(chan struct{})
	s.Changed = func() <-chan struct{} { return unchanged }
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/stream", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	next := func() string {
		for scan.Scan() {
			if data, ok := strings.CutPrefix(scan.Text(), "data: ["); ok {
				var tabs []Tab
				if err := json.Unmarshal([]byte("["+data), &tabs); err != nil {
					t.Fatal(err)
				}
				return tabs[0].Question.ID
			}
		}
		t.Fatalf("stream ended: %v", scan.Err())
		return ""
	}
	if next() != "1" {
		t.Fatal("missing first page")
	}
	page.Store(2)
	if next() != "2" {
		t.Fatal("question page did not update without an activity change")
	}
}
