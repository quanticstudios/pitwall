package pane

import (
	"context"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestSearchContext(t *testing.T) {
	p := &Pane{vt: vt.New(20, 3, nil)}
	p.vt.Write([]byte("old needle\r\nmore\r\nmore\r\nmore\r\n"))
	got, more := p.SearchContext(context.Background(), "needle", 10)
	if len(got) != 1 || got[0].Text != "old needle" || more {
		t.Fatalf("matches %+v, more %v", got, more)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := p.SearchContext(ctx, "needle", 10); len(got) != 0 {
		t.Fatal("cancelled pane search returned matches")
	}
}
