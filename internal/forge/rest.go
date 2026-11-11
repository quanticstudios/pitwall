package forge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Result is one directory's pull request, or the error that kept it unread.
type Result struct {
	PR  *model.PR
	Err error
}

type response struct {
	etag string
	body []byte
	link string
	at   time.Time
}
type pullCache struct {
	updated, head, base string
	decisionAt          string
	data                ghPR
	finished            bool
	checksAt            time.Time
}
type repoCache struct {
	at    time.Time
	pulls []restPull
}

// Poller shares conditional REST responses across a daemon's worktrees.
// All requests use gh's authentication without reading credentials.
type Poller struct {
	mu               sync.Mutex
	budgetMu         sync.Mutex
	urls             map[string]response
	repos            map[string]repoCache
	invalid          sync.Map
	now              func() time.Time
	pulls            map[string]pullCache
	branches         map[string]*model.PR
	remaining, limit int
	reset            time.Time
	mode             string
}

func NewPoller() *Poller {
	return &Poller{urls: map[string]response{}, repos: map[string]repoCache{}, pulls: map[string]pullCache{}, branches: map[string]*model.PR{}, now: time.Now}
}

// Invalidate makes the next poll refresh repository lists after a gh command.
func (p *Poller) Invalidate(dir string) { p.invalid.Store(dir, true) }

// Wait stretches intervals below 20% of the REST budget and stops at zero.
// A zero interval asks only whether all requests must pause.
func (p *Poller) Wait(now time.Time, interval time.Duration) time.Duration {
	return p.wait(now, interval)
}
func (p *Poller) wait(now time.Time, interval time.Duration) time.Duration {
	p.budgetMu.Lock()
	defer p.budgetMu.Unlock()
	if p.limit > 0 && now.Before(p.reset) {
		if p.remaining == 0 {
			return p.reset.Sub(now)
		}
		if p.remaining*5 < p.limit {
			return interval * 5
		}
	}
	// Replace Next's fallback pause with the reset supplied by GitHub.
	if p.mode == "paused until reset" && interval == RateLimitPause {
		return 0
	}
	return interval
}

func (p *Poller) budget(h http.Header) {
	p.budgetMu.Lock()
	defer p.budgetMu.Unlock()
	limit, e1 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	left, e2 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, e3 := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || limit <= 0 {
		return
	}
	p.limit, p.remaining, p.reset = limit, left, time.Unix(reset, 0)
	mode := "normal"
	if left == 0 {
		mode = "paused until reset"
	} else if left*5 < limit {
		mode = "slow (5x intervals)"
	}
	if mode != p.mode {
		if p.mode != "" || mode != "normal" {
			log.Printf("pull requests: REST budget %s (%d/%d remaining)", mode, left, limit)
		}
		p.mode = mode
	}
}

// get reads headers even when gh exits nonzero for HTTP 304 or an error.
func (p *Poller) get(ctx context.Context, dir, path string) (response, error) {
	path = strings.TrimPrefix(path, "/")
	if p.wait(p.now(), 0) > 0 {
		return response{}, ErrRateLimited
	}
	cached := p.urls[path]
	args := []string{"api", "--include", "--method", "GET", path}
	if cached.etag != "" {
		args = append(args, "-H", "If-None-Match: "+cached.etag)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, runerr := ghRaw(cctx, dir, args...)
	if cctx.Err() != nil {
		return response{}, cctx.Err()
	}
	res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(out)), nil)
	if err != nil {
		if runerr != nil {
			var exit *exec.ExitError
			if errors.As(runerr, &exit) && exit.ExitCode() == 4 || strings.Contains(runerr.Error(), "gh auth login") {
				return response{}, ErrNoGH
			}
			return response{}, runerr
		}
		return response{}, fmt.Errorf("gh api response: %w", err)
	}
	defer res.Body.Close()
	p.budget(res.Header)
	if res.StatusCode == http.StatusNotModified {
		if cached.body == nil {
			return response{}, errors.New("gh api: 304 without a cached response")
		}
		cached.at = p.now()
		p.urls[path] = cached
		return cached, nil
	}
	if res.StatusCode == 403 && p.wait(p.now(), 0) > 0 || res.StatusCode == 429 {
		return response{}, ErrRateLimited
	}
	if res.StatusCode == 401 {
		return response{}, ErrNoGH
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return response{}, fmt.Errorf("gh api: HTTP %d", res.StatusCode)
	}
	if runerr != nil {
		return response{}, runerr
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return response{}, err
	}
	got := response{res.Header.Get("ETag"), body, res.Header.Get("Link"), p.now()}
	p.urls[path] = got
	return got, nil
}

// restPages follows Link on cached pages too, so a 304 cannot hide page two.
func restPages[T any](ctx context.Context, p *Poller, dir, path string) ([]T, error) {
	var all []T
	for path != "" {
		r, err := p.get(ctx, dir, path)
		if err != nil {
			return nil, err
		}
		var items []T
		if err := json.Unmarshal(r.body, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		path = nextPage(r.link)
	}
	return all, nil
}

type restPull struct {
	Number     int        `json:"number"`
	State      string     `json:"state"`
	Draft      bool       `json:"draft"`
	URL        string     `json:"html_url"`
	Updated    string     `json:"updated_at"`
	MergedAt   *time.Time `json:"merged_at"`
	Mergeable  *bool      `json:"mergeable"`
	MergeState string     `json:"mergeable_state"`
	Head       struct {
		Ref, SHA string
		Repo     struct {
			FullName string `json:"full_name"`
		}
	} `json:"head"`
	Base struct{ Ref string } `json:"base"`
}

func githubRepository(remote string) (string, error) {
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		remote = "ssh://" + strings.Replace(remote, ":", "/", 1)
	}
	u, err := url.Parse(remote)
	if err != nil {
		return "", ErrNoGH
	}
	host := strings.ToLower(u.Hostname())
	// Account-specific SSH aliases keep the github.com- prefix.
	if host != "github.com" && !(u.Scheme == "ssh" && strings.HasPrefix(host, "github.com-")) {
		return "", ErrNoGH
	}
	remote = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if strings.Count(remote, "/") != 1 {
		return "", ErrNoGH
	}
	for _, part := range strings.Split(remote, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrNoGH
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.", r)) {
				return "", ErrNoGH
			}
		}
	}
	return strings.ToLower(remote), nil
}

type branchRef struct {
	dir, repo, head, name string
	number                int // gh pr checkout can track refs/pull/{n}/head.
}

func repository(ctx context.Context, dir string) (branchRef, error) {
	b := branchRef{dir: dir}
	branch, err := gitstat.Command(ctx, dir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return b, err
	}
	b.name = strings.TrimSpace(string(branch))
	out, err := gitstat.Command(ctx, dir, "config", "--get-regexp", `^(remote\..*\.(url|gh-resolved)|remote\.pushdefault|branch\..*\.(remote|pushremote|merge))$`).Output()
	if err != nil {
		return b, ErrNoGH
	}
	config := map[string]string{}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, _ := strings.Cut(line, " ")
		config[key] = value
		if strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".url") {
			remotes = append(remotes, strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url"))
		}
	}
	baseRemote := ""
	for _, remote := range remotes {
		if config["remote."+remote+".gh-resolved"] == "base" {
			baseRemote = remote
			break
		}
	}
	for _, remote := range append([]string{baseRemote, "upstream", "origin"}, remotes...) {
		if repo, e := githubRepository(config["remote."+remote+".url"]); e == nil {
			b.repo, baseRemote = repo, remote
			break
		}
	}
	if b.repo == "" {
		return b, ErrNoGH
	}
	prefix := "branch." + b.name + "."
	remote := config[prefix+"pushremote"]
	for _, candidate := range []string{config["remote.pushdefault"], config[prefix+"remote"], "origin", baseRemote} {
		if remote == "" && (candidate != "origin" || config["remote.origin.url"] != "") {
			remote = candidate
		}
	}
	if raw := config["remote."+remote+".url"]; raw != "" {
		remote = raw
	}
	b.head, err = githubRepository(remote)
	if err != nil {
		return b, err
	}
	merge := config[prefix+"merge"]
	if strings.HasPrefix(merge, "refs/heads/") {
		b.name = strings.TrimPrefix(merge, "refs/heads/")
	} else if number, ok := strings.CutPrefix(merge, "refs/pull/"); ok {
		number, ok = strings.CutSuffix(number, "/head")
		if ok {
			b.number, _ = strconv.Atoi(number)
			b.name = merge
		}
	}
	return b, nil
}

// View reads one branch through REST. Poll batches timer-driven reads.
func (p *Poller) View(ctx context.Context, dir string) (*model.PR, error) {
	r := p.Poll(ctx, []string{dir})[dir]
	return r.PR, r.Err
}

// Poll shares open pulls by base repository and matches each branch's push
// repository and upstream ref, or the PR ref recorded by gh pr checkout.
func (p *Poller) Poll(ctx context.Context, dirs []string) map[string]Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	repos := map[string][]branchRef{}
	results := map[string]Result{}
	for _, dir := range dirs {
		b, err := repository(ctx, dir)
		if err != nil {
			results[dir] = Result{Err: err}
			continue
		}
		key := b.repo + ":" + b.head + ":" + b.name
		if _, invalid := p.invalid.LoadAndDelete(dir); invalid {
			delete(p.repos, b.repo)
			if pr := p.branches[key]; pr != nil {
				path := "repos/" + b.repo + "/pulls/" + strconv.Itoa(pr.Number)
				cached := p.pulls[path]
				cached.finished = false
				p.pulls[path] = cached
			}
			delete(p.branches, key)
		}
		repos[b.repo] = append(repos[b.repo], b)
	}
	for repo, branches := range repos {
		dir := branches[0].dir
		cached := p.repos[repo]
		pulls := cached.pulls
		interval := QuietEvery
		for _, pr := range pulls {
			if c, ok := p.pulls["repos/"+repo+"/pulls/"+strconv.Itoa(pr.Number)]; ok && !c.finished {
				interval = OpenEvery
				break
			}
		}
		var err error
		if cached.at.IsZero() || !p.now().Before(cached.at.Add(p.wait(p.now(), interval))) {
			pulls, err = restPages[restPull](ctx, p, dir, "repos/"+repo+"/pulls?state=open&per_page=100")
			if err == nil {
				p.repos[repo] = repoCache{p.now().Truncate(OpenEvery), pulls}
			}
		}
		values := map[int]Result{}
		open := map[string]restPull{}
		for _, pr := range pulls {
			key := strings.ToLower(pr.Head.Repo.FullName) + ":" + pr.Head.Ref
			if pr.Number > open[key].Number {
				open[key] = pr
			}
		}
		for _, b := range branches {
			if err != nil {
				results[b.dir] = Result{Err: err}
				continue
			}
			key := repo + ":" + b.head + ":" + b.name
			pr, ok := open[b.head+":"+b.name]
			if b.number > 0 {
				for _, c := range pulls {
					if c.Number == b.number {
						pr, ok = c, true
						break
					}
				}
			}
			if !ok {
				previous, known := p.branches[key]
				if known && (previous == nil || previous.State != model.PROpen) {
					results[b.dir] = Result{PR: previous}
					continue
				}
				var closed []restPull
				var e error
				if b.number > 0 {
					var r response
					r, e = p.get(ctx, b.dir, "repos/"+repo+"/pulls/"+strconv.Itoa(b.number))
					if e == nil {
						e = json.Unmarshal(r.body, &pr)
						ok = e == nil
					}
				} else {
					head := strings.Split(b.head, "/")[0] + ":" + b.name
					closed, e = restPages[restPull](ctx, p, b.dir, "repos/"+repo+"/pulls?state=all&head="+url.QueryEscape(head)+"&per_page=100")
				}
				if e != nil {
					results[b.dir] = Result{Err: e}
					continue
				}
				for _, c := range closed {
					if c.Head.Ref == b.name && strings.EqualFold(c.Head.Repo.FullName, b.head) && (!ok || c.Number > pr.Number) {
						pr, ok = c, true
					}
				}
				if !ok {
					p.branches[key] = nil
					results[b.dir] = Result{}
					continue
				}
			}
			value, done := values[pr.Number]
			if !done {
				value.PR, value.Err = p.pull(ctx, b.dir, repo, pr)
				values[pr.Number] = value
			}
			results[b.dir] = value
			if value.Err == nil {
				p.branches[key] = value.PR
			}
		}
	}
	return results
}

func (p *Poller) pull(ctx context.Context, dir, repo string, pr restPull) (*model.PR, error) {
	path := "repos/" + repo + "/pulls/" + strconv.Itoa(pr.Number)
	old := p.pulls[path]
	g := old.data
	var err error
	var base struct{ Object struct{ SHA string } }
	base.Object.SHA = old.base
	if pr.State == "open" {
		basePath := "repos/" + repo + "/git/ref/heads/" + url.PathEscape(pr.Base.Ref)
		r := p.urls[basePath]
		if r.at.IsZero() || !p.now().Before(r.at.Add(p.wait(p.now(), OpenEvery))) {
			r, err = p.get(ctx, dir, basePath)
			if err != nil {
				return nil, err
			}
		}
		if err := json.Unmarshal(r.body, &base); err != nil {
			return nil, err
		}
	}
	if old.updated != pr.Updated || old.head != pr.Head.SHA || old.base != base.Object.SHA || g.Base != pr.Base.Ref || g.Mergeable == "UNKNOWN" || g.State != strings.ToUpper(pr.State) {
		r, err := p.get(ctx, dir, path)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(r.body, &pr); err != nil {
			return nil, err
		}
		g.Mergeable, g.MergeState = "UNKNOWN", strings.ToUpper(pr.MergeState)
		if pr.Mergeable != nil {
			if *pr.Mergeable {
				g.Mergeable = "MERGEABLE"
			} else {
				g.Mergeable = "CONFLICTING"
			}
		}
	}
	g.Number, g.State, g.IsDraft, g.URL, g.Head, g.Base = pr.Number, strings.ToUpper(pr.State), pr.Draft, pr.URL, pr.Head.SHA, pr.Base.Ref
	if pr.MergedAt != nil {
		g.State = "MERGED"
	}
	if old.updated != pr.Updated || old.head != pr.Head.SHA {
		if err := p.reviews(ctx, dir, path, &g); err != nil {
			return nil, err
		}
		if old.decisionAt != pr.Updated {
			old.data.ReviewDecision, err = reviewDecision(ctx, dir, repo, pr.Number)
			old.decisionAt = pr.Updated
			p.pulls[path] = old
			if err != nil {
				log.Printf("pull requests: #%d review decision unavailable: %q", pr.Number, err)
			}
		}
		g.ReviewDecision = old.data.ReviewDecision

		if old.head != pr.Head.SHA || len(g.Commits) == 0 {
			r, err := p.get(ctx, dir, "repos/"+repo+"/git/commits/"+pr.Head.SHA)
			if err != nil {
				return nil, err
			}
			var commit struct {
				Committer struct{ Date time.Time }
			}
			if err := json.Unmarshal(r.body, &commit); err != nil {
				return nil, err
			}
			g.Commits = nil
			g.Commits = append(g.Commits, ghCommit{commit.Committer.Date})
		}
	}
	finished := old.finished
	if old.head != pr.Head.SHA || !finished || !p.now().Before(old.checksAt.Add(p.wait(p.now(), QuietEvery))) {
		var err error
		finished, err = p.checks(ctx, dir, repo, pr.Head.SHA, &g)
		if err != nil {
			return nil, err
		}
		old.checksAt = p.now()
	}
	if old.head != "" && old.head != pr.Head.SHA {
		maps.DeleteFunc(p.urls, func(path string, _ response) bool {
			return strings.HasPrefix(path, "repos/"+repo+"/commits/"+old.head+"/") || path == "repos/"+repo+"/git/commits/"+old.head
		})
	}
	value := parse(g)
	p.pulls[path] = pullCache{updated: pr.Updated, head: pr.Head.SHA, base: base.Object.SHA, data: g, finished: finished, decisionAt: old.decisionAt, checksAt: old.checksAt}
	return &value, nil
}

func (p *Poller) reviews(ctx context.Context, dir, path string, g *ghPR) error {
	type review struct {
		ID        int64
		NodeID    string `json:"node_id"`
		User      struct{ Login string }
		State     string
		Submitted time.Time `json:"submitted_at"`
	}
	reviews, err := restPages[review](ctx, p, dir, path+"/reviews?per_page=100")
	if err != nil {
		return err
	}
	latest := map[string]review{}
	var authors []string
	for _, r := range reviews {
		if r.State == "PENDING" || r.State == "COMMENTED" {
			continue
		}
		if _, ok := latest[r.User.Login]; !ok {
			authors = append(authors, r.User.Login)
		}
		latest[r.User.Login] = r
	}
	g.Reviews = nil
	for _, author := range authors {
		r := latest[author]
		item := ghReview{ID: r.NodeID, State: r.State, SubmittedAt: r.Submitted}
		if item.ID == "" {
			item.ID = strconv.FormatInt(r.ID, 10)
		}
		item.Author.Login = author
		g.Reviews = append(g.Reviews, item)
	}
	return nil
}

func (p *Poller) checks(ctx context.Context, dir, repo, sha string, g *ghPR) (bool, error) {
	path := "repos/" + repo + "/commits/" + sha
	g.Checks = nil
	finished := true
	page := path + "/check-runs?per_page=100"
	for page != "" {
		r, err := p.get(ctx, dir, page)
		if err != nil {
			return false, err
		}
		var runs struct {
			Runs []struct{ Name, Status, Conclusion string } `json:"check_runs"`
		}
		if err := json.Unmarshal(r.body, &runs); err != nil {
			return false, err
		}
		for _, c := range runs.Runs {
			status := strings.ToUpper(c.Status)
			finished = finished && status == "COMPLETED"
			g.Checks = append(g.Checks, ghCheck{Type: "CheckRun", Name: c.Name, Status: status, Conclusion: strings.ToUpper(c.Conclusion)})
		}
		page = nextPage(r.link)
	}
	page = path + "/status?per_page=100"
	for page != "" {
		r, err := p.get(ctx, dir, page)
		if err != nil {
			return false, err
		}
		var statuses struct {
			Statuses []struct{ Context, State string }
		}
		if err := json.Unmarshal(r.body, &statuses); err != nil {
			return false, err
		}
		for _, c := range statuses.Statuses {
			state := strings.ToUpper(c.State)
			finished = finished && state != "PENDING" && state != "EXPECTED"
			g.Checks = append(g.Checks, ghCheck{Type: "StatusContext", Context: c.Context, State: state})
		}
		page = nextPage(r.link)
	}
	// No checks can mean GitHub has not created the runs yet.
	return finished && len(g.Checks) > 0, nil
}

func nextPage(link string) string {
	for _, part := range strings.Split(link, ",") {
		if strings.Contains(part, `rel="next"`) {
			raw, _, _ := strings.Cut(strings.TrimSpace(part), ">")
			u, err := url.Parse(strings.TrimPrefix(raw, "<"))
			if err == nil && u.Host == "api.github.com" {
				return u.RequestURI()
			}
		}
	}
	return ""
}

// GitHub exposes aggregate review rules only through GraphQL. The caller
// requests this field once per updated_at, including a failed attempt.
func reviewDecision(ctx context.Context, dir, repo string, number int) (string, error) {
	const query = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewDecision}}}`
	owner, name, _ := strings.Cut(repo, "/")
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := gh(cctx, dir, "api", "graphql", "-f", "query="+query, "-f", "owner="+owner, "-f", "name="+name, "-F", "number="+strconv.Itoa(number))
	if err != nil {
		return "", err
	}
	var result struct {
		Data struct {
			Repository struct {
				PullRequest struct{ ReviewDecision string }
			}
		}
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", err
	}
	return result.Data.Repository.PullRequest.ReviewDecision, nil
}
