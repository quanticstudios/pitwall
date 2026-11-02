package tmuxshim

import (
	"reflect"
	"strings"
	"testing"
)

// Adapted from tuios (MIT): internal/tmuxcompat/args_test.go
func TestRecordedSplitArguments(t *testing.T) {
	sp := Spec{Bools: "dhvP", Values: "tlFe"}
	for _, args := range [][]string{
		{"-d", "-t", "%1", "-h", "-l", "70%", "-P", "-F", "#{pane_id}", "--", "cat"},
		{"-dhP", "-t%1", "-l70%", "-F#{pane_id}", "--", "cat"},
	} {
		p, err := ParseFlags("split-window", sp, args)
		if err != nil {
			t.Fatal(err)
		}
		for _, flag := range []byte{'d', 'h', 'P'} {
			if !p.Has(flag) {
				t.Errorf("missing -%c", flag)
			}
		}
		for flag, want := range map[byte]string{'t': "%1", 'l': "70%", 'F': "#{pane_id}"} {
			if got, _ := p.Value(flag); got != want {
				t.Errorf("-%c=%q, want %q", flag, got, want)
			}
		}
		if !reflect.DeepEqual(p.Args, []string{"cat"}) {
			t.Fatal(p.Args)
		}
	}
	p, err := ParseFlags("split-window", sp, []string{"-e", "A=1", "-eB=2", "echo hi", "--flag"})
	if err != nil || !reflect.DeepEqual(p.Values('e'), []string{"A=1", "B=2"}) || !reflect.DeepEqual(p.Args, []string{"echo hi", "--flag"}) {
		t.Fatalf("%+v %v", p, err)
	}
	for _, args := range [][]string{{"-Q"}, {"-t"}} {
		if _, err := ParseFlags("split-window", sp, args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestKeys(t *testing.T) {
	for _, c := range []struct {
		args         []string
		literal, hex bool
		want         string
	}{
		{[]string{"echo hi", "Enter"}, false, false, "echo hi\r"},
		{[]string{"C-c", "M-x", "Up", "F1", "BSpace", "^@", "M-C-a"}, false, false, "\x03\x1bx\x1b[A\x1bOP\x7f\x00\x1b\x01"},
		{[]string{"Enter", "C-c"}, true, false, "EnterC-c"},
		{[]string{"S-Tab"}, false, false, "\x1b[Z"},
		{[]string{"61", "0x0d"}, false, true, "a\r"},
		{[]string{"C-Up", "unrecognized"}, false, false, "C-Upunrecognized"},
	} {
		got, err := SendKeysText(c.args, c.literal, c.hex)
		if err != nil || got != c.want {
			t.Errorf("%q = %q, %v; want %q", c.args, got, err, c.want)
		}
	}
	if _, err := SendKeysText([]string{"100"}, false, true); err == nil {
		t.Fatal("accepted non-byte hex")
	}
}

func TestFormats(t *testing.T) {
	vars := map[string]string{"pane_id": "%42", "window_id": "@7", "pane_title": "researcher", "pane_active": "1", "window_panes": "2", "session_name": "test", "pane_index": "0"}
	for _, c := range []struct{ format, want string }{
		{"#{window_id}", "@7"}, {"#{pane_id}", "%42"},
		{"#{pane_id} #{pane_title} #{pane_active}", "%42 researcher 1"},
		{"#D:#T #S #P ## #, #}", "%42:researcher test 0 # , }"},
		{"#{unknown}", ""},
		{"#{?pane_active,active,idle}", "active"},
		{"#{?missing,yes,no}", "no"},
		{"#{?#{==:#{window_panes},2},#{pane_title},none}", "researcher"},
		{"#{!=:x,y}", "1"}, {"#{==:x,y}", "0"},
		{"#{?pane_active,a#,b,c}", "a,b"},
	} {
		got, err := Expand(c.format, vars)
		if err != nil || got != c.want {
			t.Errorf("%q = %q, %v; want %q", c.format, got, err, c.want)
		}
	}
	for _, format := range []string{"#{pane_id", "#{?x,y}", "#{==:a}", "#{q:pane_title}", "#(echo unsafe)", strings.Repeat("#{?pane_active,", 40) + "a" + strings.Repeat(",b}", 40)} {
		if _, err := Expand(format, vars); err == nil {
			t.Errorf("accepted %q", format)
		}
	}
	if ID("%", "ffffffffffffffff") != "%18446744073709551615" || ID("@", "000000000000002a") != "@42" {
		t.Fatal("ids lost bits")
	}
}
