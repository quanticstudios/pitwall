package proto

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "record the current wire layout in testdata/wire.txt")

// wireFile holds the layout of every message at the Version and Level on
// its first line, one struct per line, so a change can be judged against
// it: see Version for the rule.
const wireFile = "testdata/wire.txt"

// wireLayout is each struct reachable from Messages, by name, as its
// fields' "name type" in order.
func wireLayout() map[string][]string {
	out := map[string][]string{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			if rt.Kind() == reflect.Map {
				walk(rt.Key())
			}
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || out[rt.String()] != nil {
			return
		}
		fields := []string{}
		out[rt.String()] = fields
		for i := range rt.NumField() {
			f := rt.Field(i)
			fields = append(fields, f.Name+" "+f.Type.String())
			walk(f.Type)
		}
		out[rt.String()] = fields
	}
	for _, m := range Messages {
		walk(reflect.TypeOf(m))
	}
	return out
}

func formatWire(version, level int, l map[string][]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d.%d\n", version, level)
	for _, n := range slices.Sorted(maps.Keys(l)) {
		fmt.Fprintf(&b, "%s{%s}\n", n, strings.Join(l[n], "; "))
	}
	return b.String()
}

func parseWire(s string) (version, level int, l map[string][]string, err error) {
	head, body, _ := strings.Cut(s, "\n")
	if _, err := fmt.Sscanf(head, "%d.%d", &version, &level); err != nil {
		return 0, 0, nil, fmt.Errorf("first line %q: %w", head, err)
	}
	l = map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		name, fields, ok := strings.Cut(strings.TrimSuffix(line, "}"), "{")
		if !ok {
			return 0, 0, nil, fmt.Errorf("line %q", line)
		}
		l[name] = []string{}
		if fields != "" {
			l[name] = strings.Split(fields, "; ")
		}
	}
	return version, level, l, nil
}

// removed lists what old has that cur lacks: a struct, or a field by name
// and type. Anything in it breaks a peer of the old layout.
func removed(old, cur map[string][]string) []string {
	var out []string
	for name, fields := range old {
		now, ok := cur[name]
		if !ok {
			out = append(out, name)
			continue
		}
		for _, f := range fields {
			if !slices.Contains(now, f) {
				out = append(out, name+"."+f)
			}
		}
	}
	slices.Sort(out)
	return out
}

// wireVerdict is "" when the recorded layout matches cur at version and
// level, else what to do about the difference.
func wireVerdict(recorded string, version, level int, cur map[string][]string) string {
	rv, rl, old, err := parseWire(recorded)
	if err != nil {
		return fmt.Sprintf("%s: %v", wireFile, err)
	}
	same := formatWire(rv, rl, old) == formatWire(rv, rl, cur)
	gone := removed(old, cur)
	switch {
	case same && version == rv && level == rl:
		return ""
	case len(gone) > 0 && version == rv:
		return fmt.Sprintf("breaking wire change (%s gone or changed): bump proto.Version, set proto.Level to 0, then record it with go test ./internal/proto -run TestWireFingerprint -update", strings.Join(gone, ", "))
	case !same && version == rv && level == rl:
		return "additive wire change: bump proto.Level, then record it with go test ./internal/proto -run TestWireFingerprint -update"
	}
	return fmt.Sprintf("proto is at %d.%d and %s records %d.%d: record it with go test ./internal/proto -run TestWireFingerprint -update", version, level, wireFile, rv, rl)
}

// TestWireFingerprint fails when a message's layout changes without the
// right bump: Level for an additive change, Version for a breaking one.
func TestWireFingerprint(t *testing.T) {
	cur := wireLayout()
	if *update {
		if err := os.WriteFile(wireFile, []byte(formatWire(Version, Level, cur)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(wireFile)
	if err != nil {
		t.Fatal(err)
	}
	if v := wireVerdict(string(data), Version, Level, cur); v != "" {
		t.Fatal(v)
	}
}

// TestWireVerdict checks the change kinds against a small layout.
func TestWireVerdict(t *testing.T) {
	old := map[string][]string{"proto.Hello": {"Version int", "Kind string"}, "proto.Sync": {}}
	rec := formatWire(3, 2, old)
	for _, c := range []struct {
		name           string
		cur            map[string][]string
		version, level int
		want           string // a prefix of the verdict
	}{
		{"unchanged", old, 3, 2, ""},
		{"field added", map[string][]string{"proto.Hello": {"Version int", "Kind string", "Level int"}, "proto.Sync": {}}, 3, 2, "additive"},
		{"message added", map[string][]string{"proto.Hello": {"Version int", "Kind string"}, "proto.Sync": {}, "proto.New": {}}, 3, 2, "additive"},
		{"added and bumped", map[string][]string{"proto.Hello": {"Version int", "Kind string", "Level int"}, "proto.Sync": {}}, 3, 3, "proto is at 3.3"},
		{"field retyped", map[string][]string{"proto.Hello": {"Version int64", "Kind string"}, "proto.Sync": {}}, 3, 3, "breaking"},
		{"message removed", map[string][]string{"proto.Hello": {"Version int", "Kind string"}}, 3, 2, "breaking"},
		{"removed and bumped", map[string][]string{"proto.Hello": {"Version int", "Kind string"}}, 4, 0, "proto is at 4.0"},
	} {
		if got := wireVerdict(rec, c.version, c.level, c.cur); !strings.HasPrefix(got, c.want) || (c.want == "") != (got == "") {
			t.Errorf("%s: %q, want %q...", c.name, got, c.want)
		}
	}
}
