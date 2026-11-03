package tmuxshim

import (
	"fmt"
	"strconv"
	"strings"
)

// ID preserves the full pitwall id as a tmux decimal id, without a hash collision.
func ID(prefix, id string) string {
	n, err := strconv.ParseUint(id, 16, 64)
	if err != nil {
		return prefix + id
	}
	return prefix + strconv.FormatUint(n, 10)
}

// Adapted from tuios (MIT): internal/tmuxcompat/format.go
var shortAliases = map[byte]string{
	'D': "pane_id", 'F': "window_flags", 'H': "host", 'h': "host_short",
	'I': "window_index", 'P': "pane_index", 'S': "session_name",
	'T': "pane_title", 'W': "window_name",
}

// Expand supports variables, aliases, escapes, conditionals and equality checks.
// Unknown variables expand to empty, as in tmux. Unsupported expressions fail.
func Expand(format string, vars map[string]string) (string, error) {
	return expand(format, vars, 0)
}

func expand(s string, vars map[string]string, depth int) (string, error) {
	if depth > 32 {
		return "", fmt.Errorf("tmux format nesting exceeds 32")
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '#' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		next := s[i+1]
		switch {
		case next == '#' || next == ',' || next == '}':
			b.WriteByte(next)
			i++
		case next == '{':
			end, nesting := i+2, 1
			for ; end < len(s); end++ {
				if s[end] == '#' && end+1 < len(s) {
					if s[end+1] == '{' {
						nesting++
					}
					end++
				} else if s[end] == '}' {
					nesting--
					if nesting == 0 {
						break
					}
				}
			}
			if nesting != 0 {
				return "", fmt.Errorf("unclosed tmux format")
			}
			v, err := expression(s[i+2:end], vars, depth+1)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = end
		case shortAliases[next] != "":
			b.WriteString(vars[shortAliases[next]])
			i++
		case next == '(':
			return "", fmt.Errorf("tmux command formats are unsupported")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

func expression(s string, vars map[string]string, depth int) (string, error) {
	if strings.HasPrefix(s, "?") {
		parts := formatParts(s[1:])
		if len(parts) != 3 {
			return "", fmt.Errorf("tmux conditional needs three parts")
		}
		condition := vars[parts[0]]
		if strings.Contains(parts[0], "#{") {
			var err error
			condition, err = expand(parts[0], vars, depth)
			if err != nil {
				return "", err
			}
		}
		if condition != "" && condition != "0" {
			return expand(parts[1], vars, depth)
		}
		return expand(parts[2], vars, depth)
	}
	if op, args, ok := strings.Cut(s, ":"); ok {
		if op != "==" && op != "!=" {
			return "", fmt.Errorf("unsupported tmux format modifier %q", op)
		}
		parts := formatParts(args)
		if len(parts) != 2 {
			return "", fmt.Errorf("tmux comparison needs two parts")
		}
		a, err := expand(parts[0], vars, depth)
		if err != nil {
			return "", err
		}
		b, err := expand(parts[1], vars, depth)
		if err != nil {
			return "", err
		}
		if (a == b) == (op == "==") {
			return "1", nil
		}
		return "0", nil
	}
	return vars[s], nil
}

func formatParts(s string) []string {
	var parts []string
	start, nesting := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+1 < len(s) {
			if s[i+1] == '{' {
				nesting++
			}
			i++
		} else if s[i] == '}' {
			nesting--
		} else if s[i] == ',' && nesting == 0 {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
