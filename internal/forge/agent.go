package forge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What a message to the agent keeps: MaxListed failed checks, the last
// LogLines lines of each one's log, and MaxPrompt bytes in all.
const (
	MaxListed = 20
	LogLines  = 150
	MaxPrompt = 30 << 10
)

// FailedCheck is a check that failed or was cancelled: its name, its page,
// and for a GitHub Actions job the tail of its failed steps' log, or in
// NoLog why gh could not read it.
type FailedCheck struct {
	Name, URL, Log string
	NoLog          string
}

// Thread is an unresolved review thread: the file and line it is on, its
// comments in order, and how many more it has than were read.
type Thread struct {
	Path     string
	Line     int
	Comments []Comment
	More     int
}

// Comment is one review comment, or a review's own text.
type Comment struct {
	Author, Body string
}

// ghRaw runs gh in dir with no prompt and returns its stdout, also when
// gh fails: gh pr checks exits non-zero with its JSON when a check failed.
// A failed run's error carries its stderr.
func ghRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, ErrNoGH
	}
	cmd := ghCmd(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("gh %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return out, nil
}

// Failures is the failed checks of the pull request of the branch at dir,
// the first MaxListed GitHub Actions jobs' with the tail of their logs. A
// log gh cannot read leaves its check with the reason in NoLog.
func Failures(ctx context.Context, dir string) ([]FailedCheck, error) {
	// gh pr checks exits 1 when a check failed and 8 while some run, with
	// the JSON on stdout all the same.
	out, err := ghRaw(ctx, dir, "pr", "checks", "--json", "name,bucket,link")
	checks, perr := parseChecks(out)
	if perr != nil {
		if err != nil {
			return nil, err
		}
		return nil, perr
	}
	for i, c := range checks[:min(len(checks), MaxListed)] {
		job := jobID(c.URL)
		if job == "" {
			continue
		}
		jctx, cancel := context.WithTimeout(ctx, time.Minute)
		checks[i].Log, err = ghTail(jctx, dir, LogLines, "run", "view", "--job", job, "--log-failed")
		cancel()
		if err != nil {
			checks[i].NoLog = reason(err)
		}
	}
	return checks, ctx.Err()
}

// ghTail is like gh but keeps only the last n lines of stdout, as logTail
// reads them, so a long log never sits in memory whole.
func ghTail(ctx context.Context, dir string, n int, args ...string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", ErrNoGH
	}
	cmd := ghCmd(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	tail, rerr := logTail(out, n)
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return tail, rerr
}

// reason is err's first line, cut to fit in a check's line of a message.
func reason(err error) string {
	s, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// parseChecks reads gh pr checks --json name,bucket,link and keeps the
// checks that failed or were cancelled, as Parse counts them.
func parseChecks(data []byte) ([]FailedCheck, error) {
	var all []struct{ Name, Bucket, Link string }
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var out []FailedCheck
	for _, c := range all {
		if c.Bucket == "fail" || c.Bucket == "cancel" {
			out = append(out, FailedCheck{Name: c.Name, URL: c.Link})
		}
	}
	return out, nil
}

var jobLink = regexp.MustCompile(`/actions/runs/\d+/job/(\d+)`)

// jobID is the GitHub Actions job a check's link points at, "" for a check
// from anywhere else.
func jobID(link string) string {
	if m := jobLink.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

// logTail is the last n lines of gh run view --log-failed read from r,
// each without the job and step names and the timestamp gh puts before it.
// It holds n lines at a time, each cut at 4 KB.
func logTail(r io.Reader, n int) (string, error) {
	br := bufio.NewReader(r) // its 4 KB buffer is the longest line kept
	ring := make([]string, n)
	count := 0
	for {
		l, err := br.ReadSlice('\n')
		line := string(l)
		for err == bufio.ErrBufferFull { // the rest of a long line goes
			_, err = br.ReadSlice('\n')
		}
		if line != "" {
			ring[count%n] = strings.TrimRight(line, "\r\n")
			count++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	lines := append(ring[count%n:], ring[:count%n]...)
	if count < n {
		lines = ring[:count]
	}
	for i, l := range lines {
		if f := strings.SplitN(l, "\t", 3); len(f) == 3 {
			l = f[2]
		}
		l = strings.TrimPrefix(l, "\ufeff")
		if ts, rest, ok := strings.Cut(l, " "); ok {
			if _, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				l = rest
			}
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n"), nil
}

// FailuresPrompt is the message that asks an agent to fix pull request
// number's failed checks: the first MaxListed with their pages, then each
// log's tail, cut from the top to share MaxPrompt, and the whole message
// capped at MaxPrompt.
func FailuresPrompt(number int, checks []FailedCheck) string {
	var head strings.Builder
	fmt.Fprintf(&head, "These checks failed on pull request #%d:\n", number)
	more := max(len(checks)-MaxListed, 0)
	checks = checks[:len(checks)-more]
	logs := 0
	for _, c := range checks {
		fmt.Fprintf(&head, "- %s", c.Name)
		if c.URL != "" {
			fmt.Fprintf(&head, ": %s", c.URL)
		}
		switch {
		case c.NoLog != "":
			fmt.Fprintf(&head, " (log unavailable: %s)", c.NoLog)
		case c.Log == "":
			head.WriteString(" (no log)")
		default:
			logs++
		}
		head.WriteString("\n")
	}
	if more > 0 {
		fmt.Fprintf(&head, "- and %d more\n", more)
	}
	b := strings.Builder{}
	b.WriteString(head.String())
	if logs > 0 {
		budget := MaxPrompt - head.Len() - len(tail)
		for _, c := range checks {
			if c.Log != "" {
				budget -= len("\nThe end of 's log:\n```\n\n```\n") + len(c.Name)
			}
		}
		per := max(budget/logs, 0)
		for _, c := range checks {
			if c.Log != "" {
				fmt.Fprintf(&b, "\nThe end of %s's log:\n```\n%s\n```\n", c.Name, lastBytes(c.Log, per))
			}
		}
	}
	return capped(b.String())
}

// tail ends every message to the agent.
const tail = "\nFix these and push."

// capped is body and tail in at most MaxPrompt bytes, body cut at its end
// with a note when it has to be.
func capped(body string) string {
	if len(body)+len(tail) <= MaxPrompt {
		return body + tail
	}
	const cut = "\n[the rest is cut]\n"
	return strings.ToValidUTF8(body[:MaxPrompt-len(tail)-len(cut)], "") + cut + tail
}

// lastBytes is the end of s in at most n bytes, from the start of a line
// when s has to be cut there.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// reviewQuery asks for a page of a pull request's review threads, and its
// last 100 reviews that approve or request changes. gh api --paginate
// follows the threads' pageInfo.
const reviewQuery = `query($owner: String!, $name: String!, $number: Int!, $endCursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $endCursor) {
        pageInfo { hasNextPage endCursor }
        nodes { isResolved path line originalLine comments(first: 50) { totalCount nodes { author { login } body } } }
      }
      reviews(last: 100, states: [APPROVED, CHANGES_REQUESTED]) { nodes { author { login } state body } }
    }
  }
}`

// Reviews is pull request number's unresolved review threads, every page
// of them, and the text of each reviewer's standing change request, of
// the repository at dir.
func Reviews(ctx context.Context, dir string, number int) ([]Thread, []Comment, error) {
	out, err := ghRaw(ctx, dir, "api", "graphql", "--paginate", "--slurp",
		"-F", "owner={owner}", "-F", "name={repo}", "-F", "number="+strconv.Itoa(number), "-f", "query="+reviewQuery)
	if err != nil {
		return nil, nil, err
	}
	return parseReviews(out)
}

type ghAuthor struct {
	Login string `json:"login"`
}

// reviewPage is one page of reviewQuery's answer.
type reviewPage struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						IsResolved   bool   `json:"isResolved"`
						Path         string `json:"path"`
						Line         int    `json:"line"`
						OriginalLine int    `json:"originalLine"`
						Comments     struct {
							TotalCount int `json:"totalCount"`
							Nodes      []struct {
								Author ghAuthor `json:"author"`
								Body   string   `json:"body"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
				Reviews struct {
					Nodes []struct {
						Author ghAuthor `json:"author"`
						State  string   `json:"state"`
						Body   string   `json:"body"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// parseReviews reads reviewQuery's pages, as --slurp wraps them: the
// threads not resolved, and the non-empty change request of each reviewer
// whose latest approve-or-request review asks for changes. A dismissed
// review is in neither state, and a comment-only review does not lift an
// earlier request, as GitHub decides it.
func parseReviews(data []byte) ([]Thread, []Comment, error) {
	var pages []reviewPage
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, nil, err
	}
	var threads []Thread
	for _, p := range pages {
		for _, t := range p.Data.Repository.PullRequest.ReviewThreads.Nodes {
			if t.IsResolved {
				continue
			}
			th := Thread{Path: t.Path, Line: t.Line, More: t.Comments.TotalCount - len(t.Comments.Nodes)}
			if th.Line == 0 { // outdated: the line it was on
				th.Line = t.OriginalLine
			}
			for _, c := range t.Comments.Nodes {
				th.Comments = append(th.Comments, Comment{Author: c.Author.Login, Body: c.Body})
			}
			threads = append(threads, th)
		}
	}
	var changes []Comment
	if len(pages) > 0 {
		latest := map[string]int{} // author -> index of their last review
		reviews := pages[0].Data.Repository.PullRequest.Reviews.Nodes
		for i, r := range reviews {
			latest[r.Author.Login] = i
		}
		for i, r := range reviews {
			if latest[r.Author.Login] == i && r.State == "CHANGES_REQUESTED" && strings.TrimSpace(r.Body) != "" {
				changes = append(changes, Comment{Author: r.Author.Login, Body: r.Body})
			}
		}
	}
	return threads, changes, nil
}

// ReviewPrompt is the message that asks an agent to address pull request
// number's review: the reviews requesting changes, then each thread by
// file and line, capped at MaxPrompt. It is "" with nothing to address.
func ReviewPrompt(number int, threads []Thread, changes []Comment) string {
	if len(threads) == 0 && len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Reviewers asked for changes on pull request #%d.\n", number)
	for _, c := range changes {
		fmt.Fprintf(&b, "\n%s requested changes:\n%s\n", c.Author, strings.TrimSpace(c.Body))
	}
	for _, t := range threads {
		if t.Line > 0 {
			fmt.Fprintf(&b, "\n%s:%d\n", t.Path, t.Line)
		} else {
			fmt.Fprintf(&b, "\n%s\n", t.Path)
		}
		for _, c := range t.Comments {
			fmt.Fprintf(&b, "%s: %s\n", c.Author, strings.TrimSpace(c.Body))
		}
		if t.More > 0 {
			fmt.Fprintf(&b, "(%d more comments on GitHub)\n", t.More)
		}
	}
	return capped(b.String())
}
