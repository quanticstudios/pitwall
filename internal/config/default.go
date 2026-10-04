package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// defaults is the config that loads the same as no file: the default
// preset's chords, the aide-dark theme, the default fonts.
func defaults() Config {
	c := Config{Keys: presetKeys(DefaultPreset)}
	c.Keys.Preset = DefaultPreset
	c.Theme, _ = Builtin("aide-dark")
	c.Theme.Name = "aide-dark"
	c.Font = Font{DefaultUIFamily, DefaultUISize, DefaultMonoFamily, DefaultMonoSize, 1, []string{}}
	gap, margin := float64(DefaultPaneGap), float64(DefaultPaneMargin)
	c.Layout = Layout{&gap, &margin}
	on := true
	c.Term.CopyOnSelect, c.Term.Links = &on, &on
	c.Decisions = defaultDecisions()
	return c
}

// presetKeys spells a preset out as a Keys value.
func presetKeys(name string) Keys {
	b := Preset(name)
	var k Keys
	hold := Chord{Mods: b.Hold}.String()
	k.SwitcherModifier = &hold
	fill := func(v reflect.Value, m map[string][]Chord) {
		for _, f := range fields(v.Type()) {
			if f.typ == bindingType {
				bd := Binding{}
				for _, c := range m[f.name] {
					bd = append(bd, c.String())
				}
				v.Field(f.index).Set(reflect.ValueOf(bd))
			}
		}
	}
	fill(reflect.ValueOf(&k).Elem(), b.Global)
	fill(reflect.ValueOf(&k.Tab).Elem(), b.Tab)
	fill(reflect.ValueOf(&k.Pane).Elem(), b.Pane)
	return k
}

// Default is a config.toml listing every option with its default value.
// Only the preset and the theme are set; every other line is commented out,
// so the preset still decides the keys until a line is uncommented.
func Default() string {
	var b strings.Builder
	fmt.Fprintf(&b, "#:schema %s\n", SchemaPath())
	b.WriteString(`# pitwall config. Every option is listed with its default; uncomment a line
# to change it. "pitwall config check" reports mistakes, and an open window
# reloads this file when it changes.

`)
	aide := presetKeys("aide")
	writeTable(&b, reflect.ValueOf(defaults()), reflect.ValueOf(Config{Keys: aide}), "")
	return b.String()
}

func comment(b *strings.Builder, doc string) {
	line := "#"
	for _, w := range strings.Fields(doc) {
		if len(line)+1+len(w) > 78 {
			b.WriteString(line + "\n")
			line = "#"
		}
		line += " " + w
	}
	if line != "#" {
		b.WriteString(line + "\n")
	}
}

// writeTable writes v's leaves, then its sub-tables. alt holds the aide
// preset's values, noted next to each key binding.
func writeTable(b *strings.Builder, v, alt reflect.Value, path string) {
	fs := fields(v.Type())
	for _, f := range fs {
		if f.typ.Kind() == reflect.Struct {
			continue
		}
		p := path + f.name
		comment(b, f.doc)
		line := f.name + " = " + tomlValue(v.Field(f.index))
		if f.typ == bindingType {
			if a := tomlValue(alt.Field(f.index)); a != tomlValue(v.Field(f.index)) {
				line += "  # aide: " + a
			}
		}
		if p != "keys.preset" && p != "theme.name" {
			line = "# " + line
		}
		b.WriteString(line + "\n\n")
	}
	for _, f := range fs {
		if f.typ.Kind() != reflect.Struct {
			continue
		}
		comment(b, f.doc)
		fmt.Fprintf(b, "[%s]\n\n", path+f.name)
		writeTable(b, v.Field(f.index), alt.Field(f.index), path+f.name+".")
	}
}

func tomlValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return `""`
		}
		return tomlValue(v.Elem())
	case reflect.String:
		return strconv.Quote(v.String())
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64)
	case reflect.Slice:
		if v.Type() == bindingType && v.Len() == 1 {
			return tomlValue(v.Index(0))
		}
		var parts []string
		for i := range v.Len() {
			parts = append(parts, tomlValue(v.Index(i)))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprint(v.Interface())
}
