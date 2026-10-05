package proto

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// wireFingerprint is the hash of every message's type and field layout at
// the current Version. When this test fails, a message changed: bump
// Version in proto.go, then paste the new hash here.
const wireFingerprint = "v12:ed134ada021d485b3ba300821a39814ff67ffdebb6ca169e444d1fb86cd235a6"

func TestWireFingerprint(t *testing.T) {
	var b strings.Builder
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			if rt.Kind() == reflect.Map {
				walk(rt.Key())
			}
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		fmt.Fprintf(&b, "%s{", rt.String())
		for i := range rt.NumField() {
			f := rt.Field(i)
			fmt.Fprintf(&b, "%s %s;", f.Name, f.Type.String())
		}
		b.WriteString("}\n")
		for i := range rt.NumField() {
			walk(rt.Field(i).Type)
		}
	}
	for _, m := range Messages {
		walk(reflect.TypeOf(m))
	}
	got := fmt.Sprintf("v%d:%x", Version, sha256.Sum256([]byte(b.String())))
	if got != wireFingerprint {
		t.Fatalf("wire messages changed: bump proto.Version, then set wireFingerprint = %q", got)
	}
}
