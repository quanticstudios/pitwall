package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Issue is an issue of a repository, as gh issue list --json IssueFields
// prints it.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// IssueFields is what Issues and IssueView ask gh for.
const IssueFields = "number,title,url,body"

// MaxIssues is how many issues Issues lists.
const MaxIssues = 50

// Issues is the open issues of the repository at dir, newest first.
// ErrNoGH also covers a repository with issues turned off.
func Issues(ctx context.Context, dir string) ([]Issue, error) {
	out, err := gh(ctx, dir, "issue", "list", "--state", "open", "--limit", strconv.Itoa(MaxIssues), "--json", IssueFields)
	if err != nil {
		return nil, err
	}
	return ParseIssues(out)
}

// IssueView is issue n of the repository at dir.
func IssueView(ctx context.Context, dir string, n int) (Issue, error) {
	var i Issue
	out, err := gh(ctx, dir, "issue", "view", strconv.Itoa(n), "--json", IssueFields)
	if err == nil {
		err = json.Unmarshal(out, &i)
	}
	return i, err
}

// ParseIssues reads gh issue list --json IssueFields, newest first.
func ParseIssues(data []byte) ([]Issue, error) {
	var is []Issue
	if err := json.Unmarshal(data, &is); err != nil {
		return nil, err
	}
	slices.SortFunc(is, func(a, b Issue) int { return b.Number - a.Number })
	return is, nil
}

// MaxBody is how many bytes of an issue's body Prompt keeps. The prompt is
// one argument of the agent's command, which Linux caps at 128 KiB, and a
// body can be up to about 256 KB.
const MaxBody = 32 << 10

// Prompt is a task prompt for i: its number and title on the first line,
// which titles the task, then its URL and body, cut at MaxBody bytes.
func (i Issue) Prompt() string {
	body := strings.TrimSpace(strings.ReplaceAll(i.Body, "\r\n", "\n"))
	if len(body) > MaxBody {
		n := MaxBody
		for n > 0 && !utf8.RuneStart(body[n]) {
			n--
		}
		body = body[:n] + "\n\n(issue body cut here; read the rest at " + i.URL + ")"
	}
	return strings.TrimSpace(fmt.Sprintf("GitHub issue #%d: %s\n%s\n\n%s", i.Number, strings.TrimSpace(i.Title), i.URL, body))
}
