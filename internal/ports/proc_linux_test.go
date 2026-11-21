package ports

import "testing"

func TestParseStat(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Proc
		ok   bool
	}{
		{"42 (node) S 7 42 3 34816 42 4194304", Proc{42, 7, 42, 3, "node"}, true},
		{"42 (my (odd) name) R 7 8 9 0", Proc{42, 7, 8, 9, "my (odd) name"}, true},
		{"42 (node) Z 7 8 9 0", Proc{}, false},
		{"42 (node) S 7", Proc{}, false},
		{"garbage", Proc{}, false},
	} {
		got, ok := parseStat(42, tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseStat(%q) = %+v %v, want %+v %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
