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

	"gopkg.in/yaml.v3"
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

// MCPServer reads a server's YAML settings and masks secrets before returning
// preview rows. Parsing YAML keeps multiline values, aliases and flow maps
// from being mistaken for setting names.
func MCPServer(text, name string) []KV {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return nil
	}
	var find func(*yaml.Node) *yaml.Node
	find = func(n *yaml.Node) *yaml.Node {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "servers" {
					servers := n.Content[i+1]
					for j := 0; j+1 < len(servers.Content); j += 2 {
						if servers.Content[j].Value == name {
							return servers.Content[j+1]
						}
					}
				}
			}
		}
		for _, child := range n.Content {
			if server := find(child); server != nil {
				return server
			}
		}
		return nil
	}
	server := find(&doc)
	if server == nil {
		return nil
	}
	var fields map[string]any
	if server.Decode(&fields) != nil {
		return nil
	}
	var rows []KV
	// Keep the source's order for ordinary fields; include merged fields too.
	seen := map[string]bool{}
	for i := 0; i+1 < len(server.Content); i += 2 {
		k := server.Content[i].Value
		if k != "<<" {
			rows = append(rows, KV{k, mcpValue(k, fields[k])})
			seen[k] = true
		}
	}
	for _, k := range sortedKeys(fields) {
		if !seen[k] {
			rows = append(rows, KV{k, mcpValue(k, fields[k])})
		}
	}
	return rows
}

func mcpValue(key string, value any) string {
	lower := strings.ToLower(key)
	if lower == "env" || lower == "headers" {
		if entries, ok := value.(map[string]any); ok {
			var parts []string
			for _, k := range sortedKeys(entries) {
				parts = append(parts, k+" = "+Mask)
			}
			return strings.Join(parts, ", ")
		}
		return Mask
	}
	if strings.Contains(lower, "token") {
		return Mask
	}
	switch v := value.(type) {
	case map[string]any:
		var parts []string
		for _, k := range sortedKeys(v) {
			parts = append(parts, k+": "+mcpValue(k, v[k]))
		}
		return strings.Join(parts, ", ")
	case []any:
		var parts []string
		for _, item := range v {
			parts = append(parts, mcpValue("", item))
		}
		return strings.Join(parts, " ")
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
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
	if ownerRepo.MatchString(rest) || !strings.Contains(s, "://") && !strings.Contains(strings.Split(rest, "/")[0], ".") {
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
