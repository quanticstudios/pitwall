package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestStatusReplyValidation(t *testing.T) {
	r := &sshHost{name: "box", dest: "box"}
	header := fmt.Sprintf("%s\t%d\t%d\t%s\t/socket\n", remoteMark, proto.Version, proto.Level, versionString())
	for _, tc := range []struct {
		name, reply string
	}{
		{"missing daemon", header},
		{"daemon error", header + daemonMark + "\ti/o timeout\n"},
		{"invalid level", strings.Replace(header, fmt.Sprintf("\t%d\t", proto.Level), "\tinvalid\t", 1) + daemonMark + "\tnot running\n"},
		{"invalid protocol", remoteMark + "\tinvalid\t0\tv1\t/socket\n" + daemonMark + "\tnot running\n"},
		{"duplicate header", header + remoteMark + "\tinvalid\t0\tv1\t/socket\n" + daemonMark + "\tnot running\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := r.parseStatus(tc.reply, "", nil, false)
			if st.OK || !st.Reachable || st.Error == "" {
				t.Fatalf("invalid reply reported healthy: %+v", st)
			}
		})
	}
	st := r.parseStatus("private shell startup output\nAPI_TOKEN=example-secret\n", "", nil, false)
	if st.OK || strings.Contains(st.Error, "private shell") || strings.Contains(st.Error, "example-secret") {
		t.Fatalf("malformed reply leaked stdout: %+v", st)
	}
}

func TestStatusSSHArgumentBoundaries(t *testing.T) {
	for _, dest := range []string{"box", "box -oProxyCommand=example", "box; echo example", "box'$(echo example)"} {
		r := &sshHost{name: "-oProxyCommand=example", dest: dest, batch: true}
		command := strings.Replace(startCommand(false), "remote-start", "remote-status", 1)
		args := r.sshArgs(nil, command)
		i := slices.Index(args, "--")
		if i < 0 || !slices.Equal(args[i:], []string{"--", dest, command}) || !slices.Contains(args[:i], "BatchMode=yes") {
			t.Fatalf("unsafe SSH argument boundaries: %q", args)
		}
	}
}

func TestSSHTailRedaction(t *testing.T) {
	in := "discard this line\nAPI_TOKEN=example-secret\nAuthorization: Bearer example-credential\nHost key verification failed.\n"
	out := sshTail(in)
	if strings.Contains(out, "example-secret") || strings.Contains(out, "example-credential") || strings.Contains(out, "discard") || !strings.Contains(out, "Host key verification failed.") {
		t.Fatalf("unsafe diagnostic: %q", out)
	}
	url := "# To authenticate, visit: https://login.example.com/a/1"
	if out := sshTail(url); out != url {
		t.Fatalf("login URL changed: %q", out)
	}
}

func TestStatusOutputLimit(t *testing.T) {
	var out statusOutput
	want := ""
	for _, s := range []string{"prefix", strings.Repeat("a", statusOutputLimit*2), "last line\n"} {
		want += s
		if n, err := out.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if len(want) > statusOutputLimit {
			want = want[len(want)-statusOutputLimit:]
		}
		if string(out.data) != want {
			t.Fatal("output does not retain the bounded tail")
		}
	}
}
