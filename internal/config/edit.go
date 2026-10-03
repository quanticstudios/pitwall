package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// SetKey sets key in [table] of the TOML file at path to value, a TOML
// literal ("14", `"Geist"`, `["Ctrl+T"]`). It replaces the value of an
// existing `key = ...` line, else uncomments a `# key = ...` default in the
// table, else appends the key to the table, creating the table at the end
// of the file. Every other byte stays as it was.
func SetKey(path, table, key, value string) error { return editFile(path, table, key, &value) }

// RemoveKey deletes key's line (all of them, for a multi-line value) from
// [table], so the key falls back to its default.
func RemoveKey(path, table, key string) error { return editFile(path, table, key, nil) }

// Quote is s as a TOML basic string.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// BindingValue is the TOML literal for a binding: one chord as a string,
// otherwise an array ([] unbinds).
func BindingValue(cs []Chord) string {
	if len(cs) == 1 {
		return Quote(cs[0].String())
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = Quote(c.String())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Number is the TOML literal for v.
func Number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func editFile(path, table, key string, value *string) error {
	target, err := resolveLinks(path)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	out := editTOML(string(old), table, key, value)
	if out == string(old) {
		return nil
	}
	// A file that parsed before must still parse. One that did not keeps
	// its errors; the edit touched only this key.
	var m map[string]any
	if _, err := toml.Decode(out, &m); err != nil {
		if _, errOld := toml.Decode(string(old), &m); errOld == nil {
			return fmt.Errorf("setting %s.%s would break %s: %v", table, key, filepath.Base(path), err)
		}
	}
	return writeFile(target, []byte(out), mode)
}

// resolveLinks follows symlinks at path, even a dangling last one, so the
// write lands in the file the link points at and the link stays a link.
func resolveLinks(path string) (string, error) {
	for range 40 {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
		t, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(path), t)
		}
		path = t
	}
	return "", fmt.Errorf("%s: too many symlinks", path)
}

// writeFile replaces path atomically: a temp file in the same directory
// with the old mode, renamed over it.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

var headerRe = regexp.MustCompile(`^\s*\[\s*([^\]\[]+?)\s*\]\s*(#.*)?$`)

// editTOML is SetKey (value set) or RemoveKey (nil) on the text src.
func editTOML(src, table, key string, value *string) string {
	commentRe := regexp.MustCompile(`^(\s*)#\s*` + regexp.QuoteMeta(key) + `\s*=`)
	cur := ""
	tableSeen := table == ""
	insertAt := 0 // just past the table's last header or key line
	keyStart, valStart, valEnd, stmtEnd := -1, 0, 0, 0
	type cand struct{ start, end, after int }
	var cands []cand
	for pos := 0; pos < len(src); {
		lineEnd := len(src)
		if i := strings.IndexByte(src[pos:], '\n'); i >= 0 {
			lineEnd = pos + i + 1
		}
		line := strings.TrimRight(src[pos:lineEnd], "\r\n")
		if m := headerRe.FindStringSubmatch(line); m != nil {
			cur = clean(m[1])
			if cur == table {
				tableSeen, insertAt = true, lineEnd
			}
			pos = lineEnd
			continue
		}
		if m := keyRe.FindStringIndex(line); m != nil {
			vs := pos + m[1]
			ve := valueEnd(src, vs)
			end := len(src)
			if i := strings.IndexByte(src[ve:], '\n'); i >= 0 {
				end = ve + i + 1
			}
			if cur == table {
				if keyStart < 0 && clean(line[:m[1]-1]) == key {
					keyStart, valStart, valEnd, stmtEnd = pos, vs, ve, end
				}
				insertAt = end
			}
			pos = end
			continue
		}
		if cur == table {
			if m := commentRe.FindStringIndex(line); m != nil {
				cands = append(cands, cand{pos, pos + len(line), pos + m[1]})
			}
		}
		pos = lineEnd
	}

	if value == nil {
		if keyStart < 0 {
			return src
		}
		return src[:keyStart] + src[stmtEnd:]
	}
	if keyStart >= 0 {
		for valStart < valEnd && (src[valStart] == ' ' || src[valStart] == '\t') {
			valStart++
		}
		return src[:valStart] + *value + src[valEnd:]
	}
	for _, c := range cands {
		// A default line holds exactly one value and maybe a comment;
		// prose that happens to start with "key =" does not.
		ve := valueEnd(src[:c.end], c.after)
		var m map[string]any
		if _, err := toml.Decode("x ="+src[c.after:c.end], &m); err != nil {
			continue
		}
		indent := commentRe.FindStringSubmatch(src[c.start:c.end])[1]
		return src[:c.start] + indent + key + " = " + *value + src[ve:]
	}
	line := key + " = " + *value + "\n"
	if tableSeen {
		if insertAt == len(src) && src != "" && !strings.HasSuffix(src, "\n") {
			line = "\n" + line
		}
		return src[:insertAt] + line + src[insertAt:]
	}
	if src != "" {
		if !strings.HasSuffix(src, "\n") {
			src += "\n"
		}
		src += "\n"
	}
	return src + "[" + table + "]\n" + line
}

// valueEnd is the index just past the TOML value starting at i: through
// strings and nested arrays or inline tables, which may span lines, and
// before a trailing comment and the spaces in front of it.
func valueEnd(s string, i int) int {
	depth, last := 0, i
	for i < len(s) {
		c := s[i]
		switch {
		case strings.HasPrefix(s[i:], `"""`) || strings.HasPrefix(s[i:], `'''`):
			q := s[i : i+3]
			j := strings.Index(s[i+3:], q)
			if j < 0 {
				return len(s)
			}
			i += 6 + j
			for i < len(s) && s[i] == q[0] { // """a""""" ends on the last quotes
				i++
			}
			last = i
			continue
		case c == '"' || c == '\'':
			i++
			for i < len(s) && s[i] != c && s[i] != '\n' {
				if c == '"' && s[i] == '\\' {
					i++
				}
				i++
			}
			i++
			last = min(i, len(s))
			continue
		case c == '#':
			if depth <= 0 {
				return last
			}
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		case c == '\n':
			if depth <= 0 {
				return last
			}
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			last = i + 1
		}
		i++
	}
	return last
}
