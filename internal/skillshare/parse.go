package skillshare

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/model.js
// (splitFrontmatter, mcpServer, cloneUrl, parseDiscover, parseAudit,
// installResult).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	frontmatter = regexp.MustCompile(`^---\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)`)
	topKey      = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.*)$`)
	blockMark   = regexp.MustCompile(`^[>|][+-]?$`)
	quoted      = regexp.MustCompile(`^(["'])(.*)["']$`)
	contLine    = regexp.MustCompile(`^\s+\S`)
)

// SplitFrontmatter is a Markdown file's frontmatter as rows, and the text
// after it.
func SplitFrontmatter(text string) (meta []KV, body string) {
	m := frontmatter.FindStringSubmatchIndex(text)
	if m == nil {
		return nil, text
	}
	for _, line := range regexp.MustCompile(`\r?\n`).Split(text[m[2]:m[3]], -1) {
		if top := topKey.FindStringSubmatch(line); top != nil {
			v := top[2]
			if blockMark.MatchString(v) {
				v = ""
			}
			meta = append(meta, KV{top[1], quoted.ReplaceAllString(v, "$2")})
		} else if len(meta) > 0 && contLine.MatchString(line) {
			last := &meta[len(meta)-1]
			last.Value = strings.TrimSpace(last.Value + " " + strings.TrimPrefix(strings.TrimSpace(line), "- "))
		}
	}
	return meta, text[m[1]:]
}

// Mask is what a secret's value shows as.
const Mask = "••••••••"

// secretKey names the fields whose values never reach the UI.
var secretKey = map[string]bool{"env": true, "headers": true, "bearerToken": true}

// MCPServer is server name's settings out of the MCP source file text
// (YAML), as rows, nil when the file does not name it. Under env, headers
// and bearerToken only the names show: every value is Mask. The file can
// hold several server lists, the global one and one per project root; the
// first that names it wins.
func MCPServer(text, name string) []KV {
	var lines []string
	for _, l := range regexp.MustCompile(`\r?\n`).Split(text, -1) {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			lines = append(lines, l)
		}
	}
	depth := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	bare := func(k string) string { return quoted.ReplaceAllString(strings.TrimSpace(k), "$2") }
	start, child := -1, -1
	for at := 0; at < len(lines) && start < 0; at++ {
		if strings.TrimSpace(lines[at]) != "servers:" {
			continue
		}
		child = -1
		for j := at + 1; j < len(lines) && depth(lines[j]) > depth(lines[at]); j++ {
			if child < 0 {
				child = depth(lines[j])
			}
			if depth(lines[j]) == child && bare(strings.TrimSuffix(strings.TrimSpace(lines[j]), ":")) == name {
				start = j
				break
			}
		}
	}
	if start < 0 {
		return nil
	}
	type row struct {
		key          string
		secret       bool
		parts        []string
		inner        int
		innerDefined bool
	}
	var rows []*row
	field := -1
	for j := start + 1; j < len(lines) && depth(lines[j]) > child; j++ {
		if field < 0 {
			field = depth(lines[j])
		}
		s := strings.TrimSpace(lines[j])
		if depth(lines[j]) == field {
			k, v, ok := strings.Cut(s, ":")
			if !ok {
				continue
			}
			r := &row{key: bare(k), secret: secretKey[bare(k)]}
			if v = strings.TrimSpace(v); v != "" {
				if r.secret {
					v = Mask
				} else {
					v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
				}
				r.parts = append(r.parts, v)
			}
			rows = append(rows, r)
			continue
		}
		if len(rows) == 0 {
			continue
		}
		last := rows[len(rows)-1]
		if !last.innerDefined {
			last.inner, last.innerDefined = depth(lines[j]), true
		}
		if depth(lines[j]) != last.inner {
			continue // deeper: a fromEnv reference or a nested map
		}
		item := strings.TrimPrefix(s, "- ")
		if last.secret {
			k, _, _ := strings.Cut(item, ":")
			last.parts = append(last.parts, bare(k)+" = "+Mask)
		} else {
			last.parts = append(last.parts, bare(item))
		}
	}
	out := make([]KV, 0, len(rows))
	for _, r := range rows {
		sep := " "
		if r.secret {
			sep = ", "
		}
		out = append(out, KV{r.key, strings.Join(r.parts, sep)})
	}
	return out
}

// Clone is what git clones for an install source.
type Clone struct {
	URL, Branch string
}

var ownerRepo = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// CloneURL turns "owner/repo", "github.com/owner/repo" or a URL into
// something git can clone; ok is false when source names no repository.
func CloneURL(source string) (Clone, bool) {
	s := strings.TrimSuffix(strings.TrimSpace(source), "/")
	if s == "" || strings.HasPrefix(s, "-") {
		return Clone{}, false
	}
	for _, p := range []string{"git@", "ssh://", "file://", "/", "~"} {
		if strings.HasPrefix(s, p) {
			return Clone{URL: s}, true
		}
	}
	rest := urlPrefix.ReplaceAllString(s, "")
	if ownerRepo.MatchString(rest) {
		rest = "github.com/" + rest
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return Clone{}, false
	}
	c := Clone{URL: "https://" + strings.TrimSuffix(strings.Join(parts[:3], "/"), ".git") + ".git"}
	if len(parts) > 4 && (parts[3] == "tree" || parts[3] == "blob") {
		c.Branch = parts[4]
	}
	return c, true
}

// ParseDiscover reads the dry run's install plan. It never falls back to
// looking for skills itself: that could audit one folder and install
// another.
func ParseDiscover(res Result) Plan {
	var j struct {
		Error   string    `json:"error"`
		Planned *[]Choice `json:"planned"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &j); err != nil || res.Code != 0 && j.Error == "" {
		return Plan{Err: Failure(res, "skillshare install --dry-run")}
	}
	if j.Error != "" {
		return Plan{Err: LastLine(j.Error)}
	}
	if j.Planned == nil {
		return Plan{Err: "Upgrade skillshare: this CLI does not give an install plan."}
	}
	seen := map[string]bool{}
	for _, c := range *j.Planned {
		if c.Name == "" || c.Path == "" {
			return Plan{Err: "skillshare returned an incomplete install plan."}
		}
		k := strings.ToLower(c.Name)
		if seen[k] {
			return Plan{Err: "Ambiguous install name: " + c.Name + ". Use the skill's own URL instead."}
		}
		seen[k] = true
	}
	return Plan{Choices: *j.Planned}
}

// AuditPart is one skill's `skillshare audit --json` output.
type AuditPart struct{ Folder, Stdout string }

// Finding is one problem the audit found in a skill it blocks.
type Finding struct {
	Skill, Severity, Pattern, File, Snippet string
	Line                                    int
}

// AuditResult is whether the audit would stop the install. Unknown: no
// part answered (an older CLI), and the install still audits on its own.
type AuditResult struct {
	Unknown  bool
	Blocked  []string
	Findings []Finding
}

var severity = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "INFO"}

// ParseAudit reads each part; prefix is the clone's folder, cut from the
// findings' file names.
func ParseAudit(parts []AuditPart, prefix string) AuditResult {
	out := AuditResult{Unknown: true}
	for _, p := range parts {
		var j struct {
			Results []struct {
				IsBlocked bool `json:"isBlocked"`
				Findings  []struct {
					Severity, Pattern, File, Snippet, Message string
					Line                                      int
				} `json:"findings"`
			} `json:"results"`
		}
		if json.Unmarshal([]byte(p.Stdout), &j) != nil || j.Results == nil {
			continue
		}
		out.Unknown = false
		skill := p.Folder[strings.LastIndex(p.Folder, "/")+1:]
		for _, r := range j.Results {
			if !r.IsBlocked {
				continue
			}
			out.Blocked = append(out.Blocked, skill)
			for _, f := range r.Findings {
				out.Findings = append(out.Findings, Finding{Skill: skill, Severity: f.Severity, Pattern: f.Pattern,
					File: strings.TrimPrefix(f.File, prefix+"/"), Line: f.Line, Snippet: firstOf(f.Snippet, f.Message)})
			}
		}
	}
	slices.SortStableFunc(out.Findings, func(a, b Finding) int {
		return slices.Index(severity, a.Severity) - slices.Index(severity, b.Severity)
	})
	return out
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// InstallResult reads an install. The exit code decides, even when an
// older CLI's JSON has no error; a skill it skipped or failed is a failure.
func InstallResult(res Result) (done []string, errText string) {
	var j struct {
		Error   string   `json:"error"`
		Skills  []string `json:"skills"`
		Failed  []string `json:"failed"`
		Skipped []string `json:"skipped"`
	}
	parsed := json.Unmarshal([]byte(res.Stdout), &j) == nil
	switch {
	case j.Error != "":
		return nil, LastLine(j.Error)
	case res.Code != 0:
		var parts []string
		if len(j.Skills) > 0 {
			parts = append(parts, "Installed: "+strings.Join(j.Skills, ", ")+".")
		}
		if len(j.Failed) > 0 {
			parts = append(parts, "Failed: "+strings.Join(j.Failed, ", ")+".")
		}
		if l := LastLine(res.Stderr); l != "" {
			parts = append(parts, l)
		}
		return nil, strings.Join(append(parts, fmt.Sprintf("skillshare install exited with %d.", res.Code)), " ")
	case len(j.Failed) > 0:
		return nil, fmt.Sprintf("Installed %d; %s failed. The install's own audit can stop one.", len(j.Skills), strings.Join(j.Failed, ", "))
	case len(j.Skipped) > 0:
		return nil, fmt.Sprintf("Installed %d; %s already installed and left as is.", len(j.Skills), strings.Join(j.Skipped, ", "))
	case !parsed:
		return nil, Failure(res, "skillshare install returned no JSON")
	}
	return j.Skills, ""
}
