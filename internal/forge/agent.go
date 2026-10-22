package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What a message to the agent keeps: the last LogLines lines of each
// failed job's log, and MaxPrompt bytes in all.
const (
	LogLines  = 150
	MaxPrompt = 30 << 10
)

// FailedCheck is a check that failed or was cancelled: its name, its page,
// and for a GitHub Actions job the tail of its failed steps' log.
type FailedCheck struct {
	Name, URL, Log string
}

// Thread is an unresolved review thread: the file and line it is on, and
// its comments in order.
type Thread struct {
	Path     string
	Line     int
	Comments []Comment
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
// each GitHub Actions job's with the tail of its log. A log gh cannot read
// leaves that check without one.
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
	for i, c := range checks {
		job := jobID(c.URL)
		if job == "" {
			continue
		}
		jctx, cancel := context.WithTimeout(ctx, time.Minute)
		out, err := ghRaw(jctx, dir, "run", "view", "--job", job, "--log-failed")
		cancel()
		if err == nil {
			checks[i].Log = logTail(string(out), LogLines)
		}
	}
	return checks, ctx.Err()
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

// logTail is the last n lines of gh run view --log-failed, each without
// the job and step names and the timestamp gh puts before it.
func logTail(out string, n int) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	lines = lines[max(len(lines)-n, 0):]
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
	return strings.Join(lines, "\n")
}

// FailuresPrompt is the message that asks an agent to fix pull request
// number's failed checks: each check with its page, then each log's tail,
// cut further from the top so the whole stays under MaxPrompt.
func FailuresPrompt(number int, checks []FailedCheck) string {
	var head strings.Builder
	fmt.Fprintf(&head, "These checks failed on pull request #%d:\n", number)
	logs := 0
	for _, c := range checks {
		fmt.Fprintf(&head, "- %s", c.Name)
		if c.URL != "" {
			fmt.Fprintf(&head, ": %s", c.URL)
		}
		if c.Log == "" {
			head.WriteString(" (no log)")
		} else {
			logs++
		}
		head.WriteString("\n")
	}
	const tail = "\nFix these and push."
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
	b.WriteString(tail)
	return b.String()
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

// reviewQuery asks for a pull request's review threads and each
// reviewer's latest review.
const reviewQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes { isResolved path line originalLine comments(first: 50) { nodes { author { login } body } } }
      }
      latestReviews(first: 50) { nodes { author { login } state body } }
    }
  }
}`

// Reviews is pull request number's unresolved review threads and the text
// of each review that requests changes, of the repository at dir.
func Reviews(ctx context.Context, dir string, number int) ([]Thread, []Comment, error) {
	out, err := ghRaw(ctx, dir, "api", "graphql", "-F", "owner={owner}", "-F", "name={repo}", "-F", "number="+strconv.Itoa(number), "-f", "query="+reviewQuery)
	if err != nil {
		return nil, nil, err
	}
	return parseReviews(out)
}

type ghAuthor struct {
	Login string `json:"login"`
}

// parseReviews reads reviewQuery's answer: the threads not resolved, and
// the non-empty bodies of reviews requesting changes.
func parseReviews(data []byte) ([]Thread, []Comment, error) {
	var g struct {
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
								Nodes []struct {
									Author ghAuthor `json:"author"`
									Body   string   `json:"body"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
					LatestReviews struct {
						Nodes []struct {
							Author ghAuthor `json:"author"`
							State  string   `json:"state"`
							Body   string   `json:"body"`
						} `json:"nodes"`
					} `json:"latestReviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, nil, err
	}
	pr := g.Data.Repository.PullRequest
	var threads []Thread
	for _, t := range pr.ReviewThreads.Nodes {
		if t.IsResolved {
			continue
		}
		th := Thread{Path: t.Path, Line: t.Line}
		if th.Line == 0 { // outdated: the line it was on
			th.Line = t.OriginalLine
		}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, Comment{Author: c.Author.Login, Body: c.Body})
		}
		threads = append(threads, th)
	}
	var changes []Comment
	for _, r := range pr.LatestReviews.Nodes {
		if r.State == "CHANGES_REQUESTED" && strings.TrimSpace(r.Body) != "" {
			changes = append(changes, Comment{Author: r.Author.Login, Body: r.Body})
		}
	}
	return threads, changes, nil
}

// ReviewPrompt is the message that asks an agent to address pull request
// number's review: the reviews requesting changes, then each thread by
// file and line, cut at MaxPrompt. It is "" with nothing to address.
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
	}
	const tail = "\nFix these and push."
	s := b.String()
	if len(s)+len(tail) > MaxPrompt {
		const cut = "\n[more comments cut]\n"
		s = strings.ToValidUTF8(s[:MaxPrompt-len(tail)-len(cut)], "") + cut
	}
	return s + tail
}
