package panel

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/session"
)

// toolView is what a tool row draws: the summary, and how each part of
// its one-line header looks and acts.
type toolView struct {
	toolSummary
	mono    bool   // arg is code: a command or a pattern, in mono
	prompt  bool   // arg is a command, after a quiet "$ "
	note    string // muted text after the arg: where a search looked
	link    string // what a click on the arg opens, a path or URL; "" for none
	errLine string // a failed call's error, its first line
	bg      string // a background shell's state, see shellState
	created bool   // a Write that made a new file
	hits    []fileHit
	sources []webSource
	readNo  bool // Read's output carries its line numbers
}

// fileHit is a file a search found, with its matches; n is 0 when the
// search did not count them.
type fileHit struct {
	path string
	n    int
}

// webSource is a page a web search returned.
type webSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// describe is c's row: summarize's, with what each kind of tool shows on
// top. out is the call's output as toolOutput gives it.
func describe(c *session.ToolCall, sum toolSummary, out string) toolView {
	v := toolView{toolSummary: sum}
	in := inputMap(c.Input)
	switch c.ToolName {
	case "Bash", "bash", "exec_command", "shell_command", "shell", "run_shell_command":
		v.mono, v.prompt = true, true
		if cmd := cmp.Or(c.Command, in.str("command", "cmd")); cmd != "" {
			v.arg = cmd // all of it, made one line below
		}
		if d := in.str("description"); d != "" {
			v.verb = d
		} else {
			v.verb = ""
		}
		v.bg = c.Background
	case "Read", "read", "read_file", "NotebookRead":
		v.link = v.arg
		if len(c.Images) > 0 {
			v.result = plural(len(c.Images), "image")
		} else if lo, hi, ok := readRange(out); ok && c.Status == "completed" {
			v.readNo = true
			v.result = plural(hi-lo+1, "line")
			if in["offset"] != nil || in["limit"] != nil {
				v.result = "lines " + strconv.Itoa(lo) + "–" + strconv.Itoa(hi)
			}
		}
	case "Grep", "grep", "search_file_content", "grep_search":
		v.verb, v.mono = "Grep", true
		v.arg = in.str("pattern")
		if p := in.str("path"); p != "" && p != "." {
			v.note = "in " + p
		}
		if c.Status == "completed" {
			var matches int
			v.hits, matches = grepHits(in.str("output_mode"), out)
			v.result = hitsLine(v.hits, matches)
		}
	case "Glob", "glob", "find":
		v.verb, v.mono = "Glob", true
		v.arg = in.str("pattern")
		if p := in.str("path"); p != "" && p != "." {
			v.note = "in " + p
		}
		if c.Status == "completed" {
			v.hits = globHits(out)
			v.result = "No files"
			if len(v.hits) > 0 {
				v.result = plural(len(v.hits), "file")
			}
		}
	case "LS", "list_directory":
		v.verb, v.icon, v.kind = "List", "folder", "other"
		if p := in.str("path"); p != "" {
			v.arg, v.path, v.link = p, true, p
		}
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		v.link = v.arg
		if len(c.Files) > 0 && c.Files[0].Status == "add" {
			v.verb, v.created = "Created", true
		}
	case "WebSearch", "web_search", "google_web_search":
		v.verb, v.arg = "Search the web", in.str("query")
		if c.Status == "completed" {
			v.sources = webSources(out)
			if len(v.sources) > 0 {
				v.result = plural(len(v.sources), "source")
			}
		}
	case "WebFetch", "web_fetch":
		v.verb = "Fetch"
		if u := in.str("url"); u != "" {
			v.arg, v.link = shortURL(u), u
		}
	case "Skill":
		v.verb, v.icon = "Using skill", "sparkles"
		v.arg = in.str("skill", "command", "name")
	case "ExitPlanMode":
		v.verb, v.icon = "Plan", "map"
		v.arg = strings.TrimLeft(firstLine(strings.TrimSpace(in.str("plan"))), "# ") // its title, without the heading's #
	case "ToolSearch":
		v.verb, v.icon = "Load tools", "search"
		v.arg = in.str("query")
	case "BashOutput", "TaskOutput", "KillShell", "KillBash", "TaskStop":
		v.verb, v.icon, v.kind = shellVerb(c.ToolName), "terminal", "other"
		v.arg, v.mono = shellRef(c), true
	}
	if server, tool, ok := mcpName(c.ToolName); ok {
		v.verb = server + " · " + tool
		v.arg = firstArg(c.Input)
	}
	if v.verb == c.ToolName && v.arg == "" { // a tool pitwall does not know
		v.arg = firstArg(c.Input)
	}
	if c.Status == "failed" {
		v.errLine = errorLine(c)
	}
	v.verb, v.arg, v.note, v.errLine = oneLine(v.verb), oneLine(v.arg), oneLine(v.note), oneLine(v.errLine)
	return v
}

// oneLine is s as a header shows it: escapes and control characters
// dropped, each run of spaces, tabs and newlines one space, and at most
// headRunes runes, since the label cuts it to the row anyway.
func oneLine(s string) string {
	s = ansi.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > headRunes {
		s = string(r[:headRunes])
	}
	return s
}

// headRunes is the most of a header's text kept: more than any row shows.
const headRunes = 400

// shellVerb is a call that reads or stops a background shell, as a verb.
func shellVerb(tool string) string {
	switch tool {
	case "BashOutput", "TaskOutput":
		return "Shell output"
	}
	return "Stop shell"
}

// hitsLine is a search's result: 12 matches in 3 files, 3 files.
func hitsLine(hits []fileHit, matches int) string {
	switch {
	case len(hits) == 0:
		return "No matches"
	case matches > 0:
		m := strconv.Itoa(matches) + " matches"
		if matches == 1 {
			m = "1 match"
		}
		return m + " in " + plural(len(hits), "file")
	}
	return plural(len(hits), "file")
}

var (
	grepFound   = regexp.MustCompile(`^Found \d+ (files?|total occurrences)`)
	grepMatch   = regexp.MustCompile(`^(.+?):(\d+):`)
	grepContext = regexp.MustCompile(`^(.+?)-(\d+)-`)
	grepCount   = regexp.MustCompile(`^(.+):(\d+)$`)
)

// grepHits reads Claude Code's Grep output in its mode: the files it
// found, and the matches when the mode counts them.
func grepHits(mode, out string) ([]fileHit, int) {
	var hits []fileHit
	at := map[string]int{}
	add := func(p string, n int) {
		i, ok := at[p]
		if !ok {
			i = len(hits)
			at[p] = i
			hits = append(hits, fileHit{path: p})
		}
		hits[i].n += n
	}
	matches := 0
	for l := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || l == "--" || grepFound.MatchString(l) || strings.HasPrefix(l, "No files found") ||
			strings.HasPrefix(l, "No matches found") || strings.HasPrefix(l, "(Results are truncated") {
			continue
		}
		switch mode {
		case "content":
			if m := grepMatch.FindStringSubmatch(l); m != nil {
				add(m[1], 1)
				matches++
			} else if grepContext.MatchString(l) {
				continue
			} else if p, _, ok := strings.Cut(l, ":"); ok && strings.ContainsAny(p, "/.") {
				add(p, 1)
				matches++
			}
		case "count":
			if m := grepCount.FindStringSubmatch(l); m != nil {
				n, _ := strconv.Atoi(m[2])
				add(m[1], n)
				matches += n
			}
		default: // files_with_matches
			add(l, 0)
		}
	}
	return hits, matches
}

// globHits is Glob's output as files.
func globHits(out string) []fileHit {
	var hits []fileHit
	for l := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "No files found") || strings.HasPrefix(l, "(Results are truncated") {
			continue
		}
		hits = append(hits, fileHit{path: l})
	}
	return hits
}

// webSources are the pages a WebSearch result lists after its "Links:"
// lines.
func webSources(out string) []webSource {
	var all []webSource
	seen := map[string]bool{}
	for rest := out; ; {
		i := strings.Index(rest, "Links: [")
		if i < 0 {
			return all
		}
		rest = rest[i+len("Links: "):]
		var got []webSource
		dec := json.NewDecoder(strings.NewReader(rest))
		if dec.Decode(&got) != nil {
			continue
		}
		rest = rest[dec.InputOffset():]
		for _, s := range got {
			if s.URL != "" && !seen[s.URL] {
				seen[s.URL] = true
				all = append(all, s)
			}
		}
	}
}

// shortURL is u as a link chip says it: its host and path, without the
// scheme, www. or a trailing slash.
func shortURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return u
	}
	s := strings.TrimPrefix(p.Host, "www.") + strings.TrimSuffix(p.EscapedPath(), "/")
	return s
}

// host is u's domain, for a source row.
func host(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Host, "www.")
}

// mcpName is an MCP tool's server and tool as people say them:
// mcp__plugin_linear_linear__save_issue is Linear and save issue.
func mcpName(name string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", "", false
	}
	s, t, ok := strings.Cut(rest, "__")
	if !ok {
		return "", "", false
	}
	sw := words(s)
	if len(sw) > 2 && sw[0] == "claude" && sw[1] == "ai" {
		sw = sw[2:]
	}
	if len(sw) > 1 && sw[0] == "plugin" {
		sw = sw[1:]
	}
	var kept []string
	for i, w := range sw { // plugin_slack_slack is slack
		if i == 0 || !strings.EqualFold(w, sw[i-1]) {
			kept = append(kept, w)
		}
	}
	tw := words(t)
	if len(tw) > 1 && len(kept) > 0 && strings.EqualFold(tw[0], kept[len(kept)-1]) { // slack_send_message
		tw = tw[1:]
	}
	if len(tw) > 1 && strings.EqualFold(tw[len(tw)-1], "mcp") { // tabs_context_mcp
		tw = tw[:len(tw)-1]
	}
	server = strings.Join(kept, " ")
	if r, n := utf8.DecodeRuneInString(server); n > 0 {
		server = string(unicode.ToUpper(r)) + server[n:]
	}
	return server, strings.ToLower(strings.Join(tw, " ")), true
}

// words splits snake_case, kebab-case and camelCase.
func words(s string) []string {
	var out []string
	var cur []rune
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			if len(cur) > 0 {
				out = append(out, string(cur))
			}
			cur = nil
			continue
		case unicode.IsUpper(r) && len(cur) > 0 && (unicode.IsLower(cur[len(cur)-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1])):
			out = append(out, string(cur))
			cur = nil
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// firstArg is a tool's first argument as one short line: the first
// string its input object holds, in the order it was sent, else its
// first number or bool.
func firstArg(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	other := ""
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the key
			break
		}
		var v any
		if dec.Decode(&v) != nil {
			break
		}
		switch x := v.(type) {
		case string:
			if x = strings.TrimSpace(x); x != "" {
				return firstLine(x)
			}
		case float64:
			if other == "" {
				other = strconv.FormatFloat(x, 'f', -1, 64)
			}
		case bool:
			if other == "" {
				other = strconv.FormatBool(x)
			}
		}
	}
	return other
}

var (
	toolErrorTag = regexp.MustCompile(`</?tool_use_error>`)
	reminder     = regexp.MustCompile(`(?s)\s*<system-reminder>.*?</system-reminder>\s*`)
)

// cleanOutput is a tool's output without the notes Claude Code adds for
// the model: system reminders and error tags.
func cleanOutput(s string) string {
	return strings.TrimSpace(toolErrorTag.ReplaceAllString(reminder.ReplaceAllString(s, "\n"), ""))
}

// errorLine is a failed call's error as one line: its first line that
// says something, past a shell's "Exit code N"; a command's last, since a
// failing command prints its error after its progress.
func errorLine(c *session.ToolCall) string {
	lines := strings.Split(cleanOutput(plainOutput(c.Output)), "\n")
	if c.ExitCode != nil || c.Command != "" || c.ToolName == "Bash" {
		slices.Reverse(lines)
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || exitCodeLine.MatchString(l) {
			continue
		}
		return l
	}
	return ""
}

var exitCodeLine = regexp.MustCompile(`^Exit code -?\d+$`)

// readLine is a line of Read's output: cat -n's number, an arrow or a
// tab, the text.
var readLine = regexp.MustCompile(`^\s*(\d+)(?:→|\t)(.*)$`)

// readRange is the first and last line numbers Read's output shows,
// false when it does not number its lines.
func readRange(out string) (lo, hi int, ok bool) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	n := 0
	for _, l := range lines {
		m := readLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		k, _ := strconv.Atoi(m[1])
		if n == 0 {
			lo = k
		}
		hi = k
		n++
	}
	return lo, hi, n > 0 && n*5 >= len(lines)*4
}

// readLines are Read's output as numbered mono lines.
func readLines(out string) []monoLine {
	var ml []monoLine
	for l := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if m := readLine.FindStringSubmatch(l); m != nil {
			k, _ := strconv.Atoi(m[1])
			ml = append(ml, monoLine{new: k, text: m[2]})
			continue
		}
		ml = append(ml, monoLine{text: l})
	}
	if n := len(ml); n > 1 && ml[n-2].new > 0 && bareNumber.MatchString(ml[n-1].text) {
		ml = ml[:n-1] // the empty line after the file's last newline, its tab trimmed
	}
	return ml
}

var bareNumber = regexp.MustCompile(`^\s*\d+$`)

// prettyJSON is s indented when it is JSON, else s.
func prettyJSON(s string) string {
	t := strings.TrimSpace(s)
	if t == "" || t[0] != '{' && t[0] != '[' {
		return s
	}
	var b bytes.Buffer
	if json.Indent(&b, []byte(t), "", "  ") != nil {
		return s
	}
	return b.String()
}

// --- background shells ---

var (
	backgroundID = regexp.MustCompile(`running in background with ID: ([A-Za-z0-9_-]+)`)
	monitorID    = regexp.MustCompile(`^Monitor started \(task ([A-Za-z0-9_-]+)`)
	shellStatus  = regexp.MustCompile(`<status>([a-z_]+)</status>`)
	shellOut     = regexp.MustCompile(`(?s)<(stdout|output)>\n?(.*?)\n?</(?:stdout|output)>`)
	shellFile    = regexp.MustCompile(`/tasks/([A-Za-z0-9_-]+)\.output$`) // where Claude Code writes a shell's output
)

// shellID is the background shell a Bash call started, "" for none.
func shellID(c *session.ToolCall) string {
	if c == nil {
		return ""
	}
	re := backgroundID
	switch c.ToolName {
	case "Bash":
	case "Monitor": // a watch runs on as a shell does
		re = monitorID
	default:
		return ""
	}
	if m := re.FindStringSubmatch(c.Output); m != nil {
		return m[1]
	}
	return ""
}

// monitorEvents is what a Monitor watch saw: its output's lines after the
// first, which only says it started.
func monitorEvents(out string) string {
	_, events, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(events)
}

// shellRef is the shell a BashOutput, TaskOutput, KillShell or TaskStop
// call names.
func shellRef(c *session.ToolCall) string {
	return inputMap(c.Input).str("bash_id", "shell_id", "task_id", "id")
}

// shells is which calls read or stop a background shell that a Bash call
// in the transcript started: those draw on the shell's row, not their own.
type shells struct {
	sig      string
	attached map[int]string   // a reading or stopping call's index: the id of the shell's Bash call
	calls    map[string][]int // a shell's Bash call's id: the indexes of the calls attached to it
}

// index works the shells out once per change of content.
func (sh *shells) index(content []session.Item) {
	sig := strconv.Itoa(len(content))
	if n := len(content); n > 0 {
		sig += content[n-1].ID + content[n-1].UpdatedAt
	}
	if sig == sh.sig {
		return
	}
	sh.sig, sh.attached, sh.calls = sig, nil, nil
	started := map[string]string{}
	for i := range content {
		c := content[i].Tool
		if c == nil {
			continue
		}
		if id := shellID(c); id != "" {
			started[id] = content[i].ID
			continue
		}
		ref := shellRef(c)
		if c.ToolName == "Read" {
			m := shellFile.FindStringSubmatch(inputMap(c.Input).str("file_path"))
			if m == nil {
				continue
			}
			ref = m[1]
		}
		switch c.ToolName {
		case "BashOutput", "TaskOutput", "KillShell", "KillBash", "TaskStop", "Read":
			b, ok := started[ref]
			if !ok {
				continue
			}
			if sh.attached == nil {
				sh.attached, sh.calls = map[int]string{}, map[string][]int{}
			}
			sh.attached[i] = b
			sh.calls[b] = append(sh.calls[b], i)
		}
	}
}

// isAttached is whether item i draws on a shell's row.
func (sh *shells) isAttached(i int) bool {
	_, ok := sh.attached[i]
	return ok
}

// shellState is a background shell's state and latest output from its
// call and the calls attached to it: running, completed, failed or
// killed.
func shellState(c *session.ToolCall, attached []*session.ToolCall) (state, out string) {
	state = c.Background
	if state == "" {
		state = "running"
	}
	for _, a := range attached {
		switch a.ToolName {
		case "KillShell", "KillBash", "TaskStop":
			if a.Status == "completed" {
				state = "killed"
			}
		case "Read": // the shell's output file
			var lines []string
			for _, l := range readLines(reminder.ReplaceAllString(a.Output, "")) {
				lines = append(lines, l.text)
			}
			out = strings.TrimRight(strings.Join(lines, "\n"), "\n")
		default:
			if m := shellStatus.FindStringSubmatch(a.Output); m != nil {
				state = m[1]
			}
			if m := shellOut.FindStringSubmatch(a.Output); m != nil {
				out = m[2]
			}
		}
	}
	if c.Background != "" && c.Background != "running" {
		state = c.Background // Claude Code's own notice has the last word
	}
	return state, out
}
