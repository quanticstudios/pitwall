package daemon

import (
	"net"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestLimitResumeVersion18Peer(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	for _, kind := range []string{"cli", "gui"} {
		t.Run(kind, func(t *testing.T) {
			nc, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer nc.Close()
			nc.SetDeadline(time.Now().Add(5 * time.Second))
			c := proto.NewConn(nc)
			for _, m := range []any{proto.Hello{Version: 18, Level: 64, Kind: kind, Cwd: t.TempDir()}, proto.Sync{}} {
				if err := c.Send(m); err != nil {
					t.Fatal(err)
				}
			}
			for {
				m, err := c.Recv()
				if err != nil {
					t.Fatal(err)
				}
				switch m := m.(type) {
				case proto.Error:
					t.Fatalf("version 18 peer refused: %s", m.Message)
				case proto.StateMsg:
					return
				}
			}
		})
	}
}
