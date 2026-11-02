package main

import (
	"net"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestCommandBackendReply(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	go b.recvLoop()
	r := proto.CommandResult{Pane: "pane", ID: 7, Integrated: true, Blocks: []vt.CommandBlock{{Command: "false", ExitCode: 1, Finished: true, StatusKnown: true}}}
	if err := proto.NewConn(daemon).Send(r); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-b.Changed():
			got := b.CommandResults()
			if len(got) == 0 {
				continue
			}
			if len(got) != 1 || got[0].ID != 7 || got[0].Blocks[0].ExitCode != 1 {
				t.Fatalf("reply %+v", got)
			}
			if len(b.CommandResults()) != 0 {
				t.Fatal("reply consumed twice")
			}
			return
		case <-timer.C:
			t.Fatal("reply not received")
		}
	}
}
