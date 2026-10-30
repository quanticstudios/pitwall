package main

import (
	"net"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestSearchBackendBatches(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	b.link.Level = proto.Level
	go b.recvLoop()
	if err := b.Send(proto.SearchAll{ID: 2, Query: "needle"}); err != nil {
		t.Fatal(err)
	}
	c := proto.NewConn(daemon)
	for _, r := range []proto.SearchAllResult{
		{ID: 1, Matches: []proto.SessionMatch{{Snippet: "stale"}}},
		{ID: 2, Matches: []proto.SessionMatch{{Snippet: "needle"}}},
		{ID: 2, Done: true},
	} {
		if err := c.Send(r); err != nil {
			t.Fatal(err)
		}
	}
	var results []proto.SearchAllResult
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for len(results) < 2 {
		select {
		case <-b.Changed():
			results = append(results, b.SearchResults()...)
		case <-timer.C:
			t.Fatal("stream not received")
		}
	}
	if results[0].ID != 2 || len(results[0].Matches) != 1 || !results[1].Done || len(b.SearchResults()) != 0 {
		t.Fatalf("batches %+v", results)
	}
	if err := b.Send(proto.SearchAll{ID: 3}); err != nil {
		t.Fatal(err)
	}
	if len(b.SearchResults()) != 0 {
		t.Fatal("new request retained old results")
	}
}
