//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestFormCommand shows a form from inside a pane and checks each way it
// ends: the answers as JSON and 0, a cancel as 1, --close as 1, and a
// spec that does not parse as 2 with the line named. --update changes an
// open form.
func TestFormCommand(t *testing.T) {
	_, run := talkDaemon(t)
	run(0, "#1\n", "new", "-n", "lead", "--", "sh")
	t.Setenv("PITWALL_PANE", paneID(t, "1.1"))

	type result struct {
		code     int
		out, err string
	}
	show := func(args []string, spec string) <-chan result {
		done := make(chan result, 1)
		go func() {
			var out, errOut bytes.Buffer
			code := formCommand(args, strings.NewReader(spec), &out, &errOut)
			done <- result{code, out.String(), errOut.String()}
		}()
		return done
	}
	open := func(id string) model.Form {
		t.Helper()
		for range 200 {
			conn, err := dialCLI()
			if err != nil {
				t.Fatal(err)
			}
			st, err := syncCLI(conn)
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range st.Forms {
				if f.ID == id {
					return f
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("form %s never opened", id)
		return model.Form{}
	}
	send := func(m any) {
		t.Helper()
		conn, err := dialCLI()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := syncCLI(conn, m); err != nil {
			t.Fatal(err)
		}
	}

	done := show([]string{"--id", "q"}, `<form title="Ship?"><input id="tag"/></form>`)
	if f := open("q"); !f.Waits || f.Title != "Ship?" {
		t.Fatalf("form %+v", f)
	}
	send(proto.FormAnswer{ID: "q", Answers: `{"button":"submit","tag":"v1"}`})
	if r := <-done; r.code != 0 || r.out != `{"button":"submit","tag":"v1"}`+"\n" {
		t.Fatalf("answer: %+v", r)
	}

	done = show([]string{"--id", "c"}, `<form><input id="tag"/></form>`)
	open("c")
	send(proto.FormAnswer{ID: "c", Cancel: true})
	if r := <-done; r.code != 1 || r.out != "" {
		t.Fatalf("cancel: %+v", r)
	}

	spec := filepath.Join(t.TempDir(), "dash.xml")
	if err := os.WriteFile(spec, []byte(`<form title="Build"><progress label="Tests" value="1" max="4"/></form>`), 0o644); err != nil {
		t.Fatal(err)
	}
	done = show([]string{"--id", "d", spec}, "")
	open("d")
	next := `<form title="Build"><progress label="Tests" value="4" max="4"/></form>`
	if r := <-show([]string{"--id", "d", "--update"}, next); r.code != 0 {
		t.Fatalf("update: %+v", r)
	}
	if f := open("d"); f.Spec != next || f.Waits {
		t.Fatalf("updated %+v", f)
	}
	if r := <-show([]string{"--id", "d", "--close"}, ""); r.code != 0 {
		t.Fatalf("close: %+v", r)
	}
	if r := <-done; r.code != 1 {
		t.Fatalf("closed: %+v", r)
	}

	for _, c := range []struct {
		args       []string
		spec, want string
	}{
		{nil, "<form>\n<input label=\"x\"/>\n</form>", "line 2: <input> needs an id"},
		{[]string{"--id", "nope", "--update"}, "<form><text>x</text></form>", "no form nope is open"},
		{[]string{"--update"}, "<form/>", "usage: pitwall form"},
	} {
		if r := <-show(c.args, c.spec); r.code != 2 || !strings.Contains(r.err, c.want) {
			t.Errorf("%v %q: %+v, want 2 and %q", c.args, c.spec, r, c.want)
		}
	}
}
