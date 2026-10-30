package web

// The web protocol's JSON Schema (docs/web-protocol.schema.json) checked
// against aide's delta fixtures and against the examples in
// docs/web-protocol.md. Go's stdlib has no JSON Schema validator, so this
// file has a small one for the keywords the schema uses, and a test that
// fails when the schema uses any other.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	schemaFile   = "../../docs/web-protocol.schema.json"
	docFile      = "../../docs/web-protocol.md"
	fixtureGlob  = "../session/testdata/session-deltas/*.json"
	minFixtures  = 3
	minExamples  = 30
	defsRefStart = "#/$defs/"
)

// checked are the keywords validate enforces; annotations are the ones it
// may skip. Anything else in the schema fails TestSchemaKeywords.
var (
	checked = map[string]bool{
		"$ref": true, "type": true, "enum": true, "const": true, "required": true,
		"properties": true, "additionalProperties": true, "items": true,
		"oneOf": true, "minimum": true,
	}
	annotations = map[string]bool{
		"$schema": true, "$id": true, "$defs": true, "title": true, "description": true,
	}
)

type schema = map[string]any

func load(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func loadSchema(t *testing.T) schema {
	t.Helper()
	return load(t, schemaFile).(schema)
}

// validate returns why v does not match s, nil when it does. root holds
// the $defs that $ref points into.
func validate(root, s schema, v any, at string) []string {
	if ref, ok := s["$ref"].(string); ok {
		def, ok := root["$defs"].(schema)[strings.TrimPrefix(ref, defsRefStart)].(schema)
		if !ok || !strings.HasPrefix(ref, defsRefStart) {
			return []string{at + ": unresolved $ref " + ref}
		}
		return validate(root, def, v, at)
	}
	if ts, ok := s["type"]; ok && !hasType(ts, v) {
		return []string{fmt.Sprintf("%s: %s is not %v", at, kind(v), ts)}
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(c, v) {
		return []string{fmt.Sprintf("%s: %v is not %v", at, v, c)}
	}
	if e, ok := s["enum"].([]any); ok && !contains(e, v) {
		return []string{fmt.Sprintf("%s: %v is not one of %v", at, v, e)}
	}
	var errs []string
	if m, ok := s["minimum"].(float64); ok {
		if n, isNum := v.(float64); isNum && n < m {
			errs = append(errs, fmt.Sprintf("%s: %v is below %v", at, n, m))
		}
	}
	if obj, ok := v.(map[string]any); ok {
		for _, r := range asSlice(s["required"]) {
			if _, ok := obj[r.(string)]; !ok {
				errs = append(errs, fmt.Sprintf("%s: missing %q", at, r))
			}
		}
		props, _ := s["properties"].(schema)
		for _, k := range sortedKeys(obj) {
			if p, ok := props[k].(schema); ok {
				errs = append(errs, validate(root, p, obj[k], at+"."+k)...)
				continue
			}
			switch extra := s["additionalProperties"].(type) {
			case bool:
				if !extra {
					errs = append(errs, fmt.Sprintf("%s: unknown key %q", at, k))
				}
			case schema:
				errs = append(errs, validate(root, extra, obj[k], at+"."+k)...)
			}
		}
	}
	if items, ok := s["items"].(schema); ok {
		for i, x := range asSlice(v) {
			errs = append(errs, validate(root, items, x, fmt.Sprintf("%s[%d]", at, i))...)
		}
	}
	if alts, ok := s["oneOf"].([]any); ok {
		errs = append(errs, oneOf(root, alts, v, at)...)
	}
	return errs
}

// oneOf passes when exactly one of alts matches. When none does, it
// reports the errors of the one alternative whose const keys match v (its
// "type" or "ok"), so a delta with a bad field names that field and not 19
// mismatched types.
func oneOf(root schema, alts []any, v any, at string) []string {
	matched, claimed := 0, 0
	var claimErrs []string
	for _, a := range alts {
		errs := validate(root, a.(schema), v, at)
		if len(errs) == 0 {
			matched++
		} else if claims(root, a.(schema), v) {
			claimed++
			claimErrs = errs
		}
	}
	switch {
	case matched == 1:
		return nil
	case matched > 1:
		return []string{fmt.Sprintf("%s: matches %d of oneOf, want 1", at, matched)}
	case claimed == 1:
		return claimErrs
	default:
		return []string{fmt.Sprintf("%s: matches none of %d alternatives", at, len(alts))}
	}
}

// claims reports whether v has every const key of s with its value, or
// whether one alternative of s's oneOf claims it.
func claims(root, s schema, v any) bool {
	if ref, ok := s["$ref"].(string); ok {
		return claims(root, root["$defs"].(schema)[strings.TrimPrefix(ref, defsRefStart)].(schema), v)
	}
	obj, _ := v.(map[string]any)
	props, _ := s["properties"].(schema)
	found := false
	for k, p := range props {
		if c, ok := p.(schema)["const"]; ok {
			if !reflect.DeepEqual(obj[k], c) {
				return false
			}
			found = true
		}
	}
	if found {
		return true
	}
	for _, a := range asSlice(s["oneOf"]) {
		if claims(root, a.(schema), v) {
			return true
		}
	}
	return false
}

func hasType(ts, v any) bool {
	for _, t := range asSlice(ts) {
		switch t {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		case "number":
			if _, ok := v.(float64); ok {
				return true
			}
		case "integer":
			if n, ok := v.(float64); ok && n == math.Trunc(n) {
				return true
			}
		}
	}
	return false
}

func kind(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%T %v", v, v)
}

// asSlice returns v as a list, wrapping a lone value ("type": "string").
func asSlice(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	default:
		return []any{x}
	}
}

func contains(list []any, v any) bool {
	for _, x := range list {
		if reflect.DeepEqual(x, v) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestSchemaKeywords fails when the schema uses a keyword validate does not
// check, so a keyword added to the schema cannot pass these tests unchecked.
func TestSchemaKeywords(t *testing.T) {
	root := loadSchema(t)
	var walk func(s schema, at string)
	walk = func(s schema, at string) {
		for k, v := range s {
			if !checked[k] && !annotations[k] {
				t.Errorf("%s: keyword %q is not checked by this test's validator", at, k)
			}
			switch k {
			case "properties", "$defs":
				for name, sub := range v.(schema) {
					walk(sub.(schema), at+"/"+k+"/"+name)
				}
			case "items":
				walk(v.(schema), at+"/items")
			case "additionalProperties":
				if sub, ok := v.(schema); ok {
					walk(sub, at+"/additionalProperties")
				}
			case "oneOf":
				for i, sub := range v.([]any) {
					walk(sub.(schema), fmt.Sprintf("%s/oneOf/%d", at, i))
				}
			case "$ref":
				name := strings.TrimPrefix(v.(string), defsRefStart)
				if _, ok := root["$defs"].(schema)[name]; !ok {
					t.Errorf("%s: $ref %s names no $defs entry", at, v)
				}
			}
		}
	}
	walk(root, "#")
}

// TestFixtures validates every delta and the final snapshot of each of
// aide's fixtures, and the delta stream as the server sends it.
func TestFixtures(t *testing.T) {
	root := loadSchema(t)
	defs := root["$defs"].(schema)
	paths, err := filepath.Glob(fixtureGlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < minFixtures {
		t.Fatalf("found %d fixtures under %s, want at least %d", len(paths), fixtureGlob, minFixtures)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f := load(t, path).(map[string]any)
			deltas := f["deltas"].([]any)
			for i, d := range deltas {
				for _, e := range validate(root, defs["delta"].(schema), d, fmt.Sprintf("deltas[%d]", i)) {
					t.Error(e)
				}
			}
			for _, e := range validate(root, defs["snapshot"].(schema), f["snapshot"], "snapshot") {
				t.Error(e)
			}
			frame := map[string]any{
				"type":      "deltas",
				"sessionId": f["snapshot"].(map[string]any)["sessionId"],
				"deltas":    deltas,
			}
			for _, e := range validate(root, root, frame, "frame") {
				t.Error(e)
			}
		})
	}
}

// \r? because a Windows checkout can have CRLF line endings.
var jsonBlock = regexp.MustCompile("(?s)```json\r?\n(.*?)```")

// TestDocExamples validates every json block in docs/web-protocol.md as a
// frame, so the examples and the schema cannot drift apart.
func TestDocExamples(t *testing.T) {
	root := loadSchema(t)
	doc, err := os.ReadFile(docFile)
	if err != nil {
		t.Fatal(err)
	}
	blocks := jsonBlock.FindAllStringSubmatch(string(doc), -1)
	if len(blocks) < minExamples {
		t.Fatalf("found %d json examples in %s, want at least %d", len(blocks), docFile, minExamples)
	}
	seen := map[string]bool{}
	for i, b := range blocks {
		var v any
		if err := json.Unmarshal([]byte(b[1]), &v); err != nil {
			t.Errorf("example %d: %v\n%s", i, err, b[1])
			continue
		}
		if obj, ok := v.(map[string]any); ok {
			seen[fmt.Sprint(obj["type"])] = true
		}
		for _, e := range validate(root, root, v, fmt.Sprintf("example %d", i)) {
			t.Error(e)
		}
	}
	// Every frame type in the schema has an example.
	for _, side := range []string{"client", "server"} {
		for _, alt := range root["$defs"].(schema)[side].(schema)["oneOf"].([]any) {
			name := strings.TrimPrefix(alt.(schema)["$ref"].(string), defsRefStart)
			if !seen[name] {
				t.Errorf("no example of %q in %s", name, docFile)
			}
		}
	}
}

// TestRejects checks that the validator, and the schema, refuse bad frames.
func TestRejects(t *testing.T) {
	root := loadSchema(t)
	// delta wraps one delta's fields after its envelope in a deltas frame.
	delta := func(fields string) string {
		return `{"type": "deltas", "sessionId": "s", "deltas": [{"version": 1, "sessionId": "s", "revision": 2, "updatedAt": "t", ` + fields + `}]}`
	}
	for _, c := range []struct{ name, frame, want string }{
		{"no type", `{"id": "1"}`, `matches none of`},
		{"unknown type", `{"type": "launch", "id": "1"}`, `matches none of`},
		{"command without id", `{"type": "interrupt", "sessionId": "s"}`, `missing "id"`},
		{"unknown key", `{"type": "interrupt", "id": "1", "sessionId": "s", "force": true}`, `unknown key "force"`},
		{"bad decision", `{"type": "resolveApproval", "id": "1", "sessionId": "s", "requestId": "r", "decision": "accept"}`, `not one of`},
		{"answers not lists", `{"type": "resolveInput", "id": "1", "sessionId": "s", "requestId": "r", "answers": {"q": "main"}}`, `is not array`},
		{"failed result without code", `{"type": "result", "id": "1", "ok": false}`, `missing "code"`},
		{"seq 0", delta(`"seq": 0, "type": "input.resolve", "requestId": "r"`), `below 1`},
		{"fractional seq", delta(`"seq": 1.5, "type": "input.resolve", "requestId": "r"`), `is not integer`},
		{"unknown delta type", delta(`"seq": 2, "type": "item.remove", "itemId": "i"`), `not one of`},
		{"tool.start without tool", delta(`"seq": 2, "type": "tool.start", "item": {"id": "i", "kind": "tool-call", "provider": "claude", "text": "", "turnId": null, "updatedAt": "t"}`), `missing "tool"`},
		{"status without running", delta(`"seq": 2, "type": "status", "status": "done", "sessionState": "ready", "detail": null, "lastCompletedAt": null`), `missing "running"`},
		{"bad sessionState", delta(`"seq": 2, "type": "status", "status": "done", "sessionState": "idle", "running": false, "detail": null, "lastCompletedAt": null`), `not one of`},
		{"image without data", `{"type": "send", "id": "1", "sessionId": "s", "text": "x", "attachments": [{"kind": "image", "id": "a", "name": "a.png", "mimeType": "image/png", "sizeBytes": 3}]}`, `missing "data"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(c.frame), &v); err != nil {
				t.Fatalf("bad test frame: %v", err)
			}
			errs := validate(root, root, v, "frame")
			if len(errs) == 0 {
				t.Fatal("frame validated, want an error")
			}
			if all := strings.Join(errs, "\n"); !strings.Contains(all, c.want) {
				t.Errorf("errors do not mention %q:\n%s", c.want, all)
			}
		})
	}
}
