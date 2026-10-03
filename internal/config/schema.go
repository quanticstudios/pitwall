package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Schema is the JSON Schema (draft 2020-12) for config.toml.
func Schema() []byte { return schemaFor(reflect.TypeFor[Config](), "pitwall config.toml") }

// ThemeSchema is the JSON Schema for a themes/<name>.toml file.
func ThemeSchema() []byte { return schemaFor(reflect.TypeFor[Theme](), "pitwall theme") }

// ChordPattern matches what ParseChord accepts, spelled case by case
// because JSON Schema patterns have no case-insensitive flag.
func ChordPattern() string {
	var mods, keys []string
	for _, m := range modNames {
		for _, n := range m.names {
			mods = append(mods, anyCase(n))
		}
	}
	for _, k := range keyNames {
		for _, s := range k.spells {
			keys = append(keys, anyCase(s))
		}
	}
	return `^((` + strings.Join(mods, "|") + `)\+)*(` + strings.Join(keys, "|") + `|[!-~])$`
}

func anyCase(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) {
			fmt.Fprintf(&b, "[%c%c]", unicode.ToUpper(r), unicode.ToLower(r))
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

func schemaFor(t reflect.Type, title string) []byte {
	root := typeSchema(t)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["title"] = title
	root["$defs"] = map[string]any{
		"chord": map[string]any{
			"type": "string", "pattern": ChordPattern(),
			"description": "Modifiers (Ctrl, Alt, Shift, Super) and a key joined by +, e.g. Ctrl+Shift+T. Keys: a printable character, Space, Tab, Enter, Esc, Backspace, Delete, Home, End, PageUp, PageDown, Up, Down, Left, Right, F1-F12. Case does not matter.",
		},
		"binding": map[string]any{
			"oneOf": []any{
				map[string]any{"$ref": "#/$defs/chord"},
				map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/chord"}},
			},
		},
		"color": map[string]any{"type": "string", "pattern": hexRe.String(), "description": "#rrggbb or #rrggbbaa"},
	}
	b, _ := json.MarshalIndent(root, "", "  ")
	return append(b, '\n')
}

func typeSchema(t reflect.Type) map[string]any {
	switch t {
	case bindingType:
		return map[string]any{"$ref": "#/$defs/binding"}
	case colorType:
		return map[string]any{"$ref": "#/$defs/color"}
	case colorsType:
		return map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/color"}, "minItems": 16, "maxItems": 16}
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		for _, f := range fields(t) {
			props[f.name] = fieldSchema(f)
		}
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props}
	case reflect.Pointer:
		return typeSchema(t.Elem())
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	}
	return map[string]any{"type": "string"}
}

func fieldSchema(f field) map[string]any {
	s := typeSchema(f.typ)
	if f.doc != "" {
		s["description"] = f.doc
	}
	switch e := f.tag.Get("enum"); e {
	case "":
	case "@themes":
		// Custom theme names are free-form; the enum still drives completion.
		delete(s, "type")
		s["anyOf"] = []any{map[string]any{"enum": Themes}, map[string]any{"type": "string", "pattern": `^[^/\\]+$`}}
	default:
		s["enum"] = strings.Split(e, ",")
	}
	for _, k := range []string{"min", "max"} {
		if v, err := strconv.ParseFloat(f.tag.Get(k), 64); err == nil {
			s[k+"imum"] = v
		}
	}
	return s
}
