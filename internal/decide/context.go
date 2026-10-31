package decide

// Context retrieval for Jev preparation (gates.go), ported from aide's
// src/main/context-retrieval.ts (ENG-2934): the workspace files a prompt
// names or whose paths match its words, with the AGENTS.md and CLAUDE.md
// files that apply to them, read inside the workspace and within budgets.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	retrievalTimeout      = 1500 * time.Millisecond
	maxGitOutput          = 2_000_000
	maxCandidatePaths     = 512
	maxMandatoryFiles     = 32
	maxMandatoryBytes     = 24_000
	maxMandatoryFileBytes = 12_000
	maxSourceFileBytes    = 1_000_000
	maxCandidates         = 16
	maxCandidateBytes     = 8_000
	maxCandidateExcerpt   = 4_000
)

// instructionFiles are in aide's Set order.
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// Excerpt is aide's ContextExcerpt, its keys in aide's order.
type Excerpt struct {
	Content   string `json:"content"`
	Revision  string `json:"contentRevision"`
	EndLine   int    `json:"endLine"`
	ID        string `json:"id"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
}

// Omission is a file left out of the context and why.
type Omission struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Retrieved is aide's RetrievedContext: Mandatory goes to the provider
// whole, Candidates are what Jev scores.
type Retrieved struct {
	Candidates []Excerpt
	Mandatory  []Excerpt
	Omitted    []Omission
}

var (
	errRetrievalCancelled = errors.New("Context retrieval was cancelled.")
	errRetrievalDeadline  = errors.New("Context retrieval exceeded its 1500ms deadline.")

	ctxURLRe     = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	ctxRefRe     = regexp.MustCompile(`(?:(?:\.{1,2}|[A-Za-z0-9_.-]+)/)*[A-Za-z0-9_.-]+\.[A-Za-z0-9_-]+(?::\d+)?`)
	ctxLineRe    = regexp.MustCompile(`:(\d+)$`)
	ctxVersionRe = regexp.MustCompile(`(?i)^v?\d+(?:\.\d+)+(?:-[\w.-]+)?$`)
	ctxTermRe    = regexp.MustCompile(`[a-z0-9_]{3,}`)
	ctxEnvRe     = regexp.MustCompile(`^\.env(?:[._-].*)?$`)
	ctxSecretRe  = regexp.MustCompile(`(?:^|[._-])(secret|secrets|credential|credentials|token|password|passwd|api[-_]?key|key)(?:[._-]|$)`)
	ctxKeyFileRe = regexp.MustCompile(`(?i)\.(key|pem|p12|pfx)$`)
	ctxLeadRe    = regexp.MustCompile("^['\"`(]+")
	ctxTrailRe   = regexp.MustCompile("['\"`),.;:]+$")
)

func ctxActive(ctx context.Context, deadline time.Time) error {
	if ctx.Err() != nil {
		return errRetrievalCancelled
	}
	if time.Now().After(deadline) {
		return errRetrievalDeadline
	}
	return nil
}

func isInstruction(rel string) bool { return slices.Contains(instructionFiles, path.Base(rel)) }

func isSecretPath(rel string) bool {
	name := strings.ToLower(path.Base(rel))
	return ctxEnvRe.MatchString(name) || ctxSecretRe.MatchString(name) || ctxKeyFileRe.MatchString(name) ||
		name == "id_rsa" || name == "id_ed25519"
}

// insideRoot is aide's isInsideRoot, which also refuses a name that
// starts with "..".
func insideRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// normalizeRef is a workspace-relative slash path, false for one that
// is absolute, has a backslash or climbs out.
func normalizeRef(v string) (string, bool) {
	t := ctxTrailRe.ReplaceAllString(ctxLeadRe.ReplaceAllString(strings.TrimSpace(v), ""), "")
	if t == "" || path.IsAbs(t) || filepath.IsAbs(t) || strings.Contains(t, `\`) {
		return "", false
	}
	n := path.Clean(t)
	if n == "." || n == ".." || strings.HasPrefix(n, "../") {
		return "", false
	}
	return n, true
}

// explicitRefs are the file paths the task names, each with the line it
// names (0 for none), and the paths sorted.
func explicitRefs(task string) (map[string]int, []string) {
	refs := map[string]int{}
	// why: A URL host such as github.com looks like a file name and must not become a required file.
	for _, v := range ctxRefRe.FindAllString(ctxURLRe.ReplaceAllString(task, " "), -1) {
		base, line := v, 0
		lm := ctxLineRe.FindStringSubmatch(v)
		if lm != nil {
			base = v[:len(v)-len(lm[0])]
			line, _ = strconv.Atoi(lm[1])
		}
		n, ok := normalizeRef(base)
		if !ok || (!strings.Contains(v, "/") && ctxVersionRe.MatchString(n)) {
			continue
		}
		if _, seen := refs[n]; !seen || lm != nil {
			refs[n] = line
		}
	}
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	// shortcut: byte order stands in for aide's localeCompare, so ties between paths that differ in case or punctuation can sort apart; port the collation if that ever matters.
	slices.Sort(keys)
	return refs, keys
}

func taskTerms(task string) []string {
	terms := ctxTermRe.FindAllString(strings.ToLower(task), -1)
	slices.Sort(terms)
	return slices.Compact(terms)
}

func pathScore(rel string, terms []string) int {
	n := strings.ToLower(rel)
	base := path.Base(n)
	score := 0
	for _, t := range terms {
		switch {
		case strings.Contains(base, t):
			score += 4
		case strings.Contains(n, t):
			score += 2
		}
	}
	return score
}

// byScore sorts paths by score, highest first, then by path.
func byScore(paths []string, terms []string) {
	slices.SortStableFunc(paths, func(a, b string) int {
		if d := pathScore(b, terms) - pathScore(a, terms); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
}

func lineCount(content string) int {
	if content == "" {
		return 1
	}
	n := strings.Count(content, "\n") + 1
	if strings.HasSuffix(content, "\n") {
		n--
	}
	return n
}

func revision(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type ctxFile struct {
	bytes   []byte
	content string
	path    string
}

func excerptID(rel string, start, end int) string { return fmt.Sprintf("%s:%d-%d", rel, start, end) }

func fullExcerpt(f *ctxFile) Excerpt {
	end := lineCount(f.content)
	return Excerpt{Content: f.content, Revision: revision(f.bytes), EndLine: end, ID: excerptID(f.path, 1, end), Path: f.path, StartLine: 1}
}

// candidateExcerpt is up to maxBytes of whole lines from two lines above
// the preferred line, or above the first line with a term; nil when the
// preferred line is past the end or nothing fits.
func candidateExcerpt(f *ctxFile, terms []string, maxBytes, preferred int) *Excerpt {
	lines := strings.SplitAfter(f.content, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	best := -1
	switch {
	case preferred > 0 && preferred <= len(lines):
		best = preferred - 1
	case preferred > 0:
		return nil
	default:
		best = slices.IndexFunc(lines, func(l string) bool {
			l = strings.ToLower(l)
			return slices.ContainsFunc(terms, func(t string) bool { return strings.Contains(l, t) })
		})
	}
	start := max(0, best-2)
	var b strings.Builder
	n := 0
	for _, l := range lines[start:] {
		if b.Len()+len(l) > maxBytes {
			break
		}
		b.WriteString(l)
		n++
	}
	if b.Len() == 0 {
		return nil
	}
	return &Excerpt{Content: b.String(), Revision: revision(f.bytes), EndLine: start + n, ID: excerptID(f.path, start+1, start+n), Path: f.path, StartLine: start + 1}
}

// cappedBuffer fails a write past its cap, which ends the command.
type cappedBuffer struct {
	bytes.Buffer
	over bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > maxGitOutput {
		c.over = true
		return 0, errors.New("over the limit")
	}
	return c.Buffer.Write(p)
}

func gitRun(ctx context.Context, deadline time.Time, root string, args ...string) (string, string, error) {
	cctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = root
	var out cappedBuffer
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return "", "", errRetrievalCancelled
	case err != nil && cctx.Err() != nil:
		return "", "", errRetrievalDeadline
	case out.over:
		return "", "", errors.New("Context retrieval file listing exceeds its 2MiB limit.")
	}
	return out.String(), stderr.String(), err
}

// gitFiles lists the workspace's tracked and untracked files; nil, nil
// outside a Git repository or without git.
func gitFiles(ctx context.Context, deadline time.Time, root string) ([]string, error) {
	out, stderr, err := gitRun(ctx, deadline, root, "ls-files", "-co", "--exclude-standard", "-z")
	var ee *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, nil
	case errors.As(err, &ee) && strings.Contains(strings.ToLower(stderr), "not a git repository"):
		return nil, nil
	case err != nil:
		return nil, err
	}
	files := strings.Split(out, "\x00")
	return slices.DeleteFunc(files, func(s string) bool { return s == "" }), nil
}

func ignoredGitPaths(ctx context.Context, deadline time.Time, root string, files []string) (map[string]bool, error) {
	ignored := map[string]bool{}
	if len(files) == 0 {
		return ignored, nil
	}
	out, _, err := gitRun(ctx, deadline, root, append([]string{"check-ignore", "--no-index", "--"}, files...)...)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return ignored, nil // none is ignored
	}
	if err != nil {
		return nil, err
	}
	for l := range strings.SplitSeq(out, "\n") {
		if l != "" {
			ignored[l] = true
		}
	}
	return ignored, nil
}

// readFile reads rel under root as aide's readValidatedFile does: nil for
// a secret name, a path or symlink leading out, a symlink itself (aide
// opens with O_NOFOLLOW), anything but a regular file, a file over 1 MB
// or one with a NUL byte.
func readFile(ctx context.Context, deadline time.Time, root, rel string) (*ctxFile, error) {
	if err := ctxActive(ctx, deadline); err != nil {
		return nil, err
	}
	n, ok := normalizeRef(rel)
	if !ok || isSecretPath(n) {
		return nil, nil
	}
	requested := filepath.Join(root, filepath.FromSlash(n))
	if !insideRoot(root, requested) {
		return nil, nil
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !insideRoot(root, resolved) {
		return nil, nil
	}
	// warn: A directory swap between the checks and the open can choose a different file.
	if fi, err := os.Lstat(requested); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
		return nil, nil
	}
	f, err := os.Open(requested)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSourceFileBytes {
		return nil, nil
	}
	if err := ctxActive(ctx, deadline); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSourceFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSourceFileBytes || bytes.IndexByte(b, 0) >= 0 {
		return nil, nil
	}
	if err := ctxActive(ctx, deadline); err != nil {
		return nil, err
	}
	return &ctxFile{bytes: b, content: strings.ToValidUTF8(string(b), "�"), path: n}, nil
}

func rootInstructions(ctx context.Context, deadline time.Time, root string) ([]string, error) {
	var out []string
	for _, name := range instructionFiles {
		if err := ctxActive(ctx, deadline); err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(root, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if insideRoot(root, resolved) {
			if fi, err := os.Stat(resolved); err == nil && fi.Mode().IsRegular() {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

// applicable are the instruction files whose folder holds one of files.
func applicable(files, instructions []string) []string {
	var out []string
	for _, f := range files {
		for _, in := range instructions {
			dir := path.Dir(in)
			if (dir == "." || f == dir || strings.HasPrefix(f, dir+"/")) && !slices.Contains(out, in) {
				out = append(out, in)
			}
		}
	}
	slices.Sort(out)
	return out
}

func omit(o *[]Omission, rel, reason string) {
	if isSecretPath(rel) {
		rel = "[redacted]"
	}
	*o = append(*o, Omission{Path: rel, Reason: reason})
}

func supportedGitPath(rel string) bool { return !strings.ContainsAny(rel, "\t\r\n") }

// usable is a path context retrieval may read: no secret or odd name, and
// not out of the workspace.
func usable(rel string) bool {
	_, ok := normalizeRef(rel)
	return !isSecretPath(rel) && supportedGitPath(rel) && ok
}

func atRoot(rel string) bool { return path.Dir(rel) == "." }

// RetrieveContext is aide's retrieveContext for the workspace root and
// the task: the files the task names and the instruction files that apply
// as Mandatory, which fails the turn when one cannot be read, and the
// best-scoring other files as Candidates. It takes at most 1.5 s.
func RetrieveContext(ctx context.Context, root, task string) (Retrieved, error) {
	deadline := time.Now().Add(retrievalTimeout)
	if err := ctxActive(ctx, deadline); err != nil {
		return Retrieved{}, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Retrieved{}, err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return Retrieved{}, errors.New("Context retrieval requires a workspace directory.")
	}
	listed, err := gitFiles(ctx, deadline, root)
	if err != nil {
		return Retrieved{}, err
	}
	if err := ctxActive(ctx, deadline); err != nil {
		return Retrieved{}, err
	}
	refs, explicit := explicitRefs(task)
	files := listed
	if listed == nil {
		files, err = rootInstructions(ctx, deadline, root)
		if err != nil {
			return Retrieved{}, err
		}
		files = append(files, explicit...)
	}
	terms := taskTerms(task)
	allInstructions := slices.DeleteFunc(slices.Clone(files), func(f string) bool { return !isInstruction(f) })
	ranked := slices.DeleteFunc(slices.Clone(files), func(f string) bool {
		_, named := refs[f]
		return named || isInstruction(f) || !usable(f)
	})
	byScore(ranked, terms)
	ranked = ranked[:min(len(ranked), maxCandidatePaths)]

	var discovered []string
	add := func(fs ...string) {
		for _, f := range fs {
			if !slices.Contains(discovered, f) {
				discovered = append(discovered, f)
			}
		}
	}
	add(explicit...)
	add(slices.DeleteFunc(slices.Clone(allInstructions), func(f string) bool { return !atRoot(f) })...)
	add(applicable(append(slices.Clone(explicit), ranked...), allInstructions)...)
	add(ranked...)

	ignored := map[string]bool{}
	if listed != nil {
		inspectable := slices.DeleteFunc(slices.Clone(discovered), func(f string) bool { return !usable(f) })
		if ignored, err = ignoredGitPaths(ctx, deadline, root, inspectable); err != nil {
			return Retrieved{}, err
		}
	}
	omitted := []Omission{}
	var available []string
	for _, f := range discovered {
		_, ok := normalizeRef(f)
		switch {
		case ignored[f]:
			omit(&omitted, f, "ignored by Git")
		case isSecretPath(f):
			omit(&omitted, f, "sensitive filename")
		case !supportedGitPath(f):
			omit(&omitted, f, "unsupported filename")
		case ok:
			available = append(available, f)
		}
	}
	slices.Sort(available)
	var explicitAvailable []string
	for _, f := range explicit {
		if slices.Contains(available, f) {
			explicitAvailable = append(explicitAvailable, f)
		} else {
			omit(&omitted, f, "not a safe workspace file")
		}
	}
	instructions := slices.DeleteFunc(slices.Clone(available), func(f string) bool { return !isInstruction(f) })
	optional := slices.DeleteFunc(slices.Clone(available), func(f string) bool {
		return slices.Contains(explicitAvailable, f) || isInstruction(f)
	})
	byScore(optional, terms)
	optional = optional[:min(len(optional), maxCandidates)]
	mandatoryPaths := slices.DeleteFunc(slices.Clone(instructions), func(f string) bool { return !atRoot(f) })
	mandatoryPaths = append(mandatoryPaths, explicitAvailable...)
	mandatoryPaths = append(mandatoryPaths, applicable(append(slices.Clone(explicitAvailable), optional...), instructions)...)
	slices.Sort(mandatoryPaths)
	mandatoryPaths = slices.Compact(mandatoryPaths)
	if len(mandatoryPaths) > maxMandatoryFiles {
		return Retrieved{}, fmt.Errorf("Required context has %d files and exceeds the %d-file limit.", len(mandatoryPaths), maxMandatoryFiles)
	}

	out := Retrieved{Candidates: []Excerpt{}, Mandatory: []Excerpt{}}
	mandatoryBytes := 0
	for _, rel := range mandatoryPaths {
		f, err := readFile(ctx, deadline, root, rel)
		if err != nil {
			return Retrieved{}, err
		}
		if f == nil {
			return Retrieved{}, fmt.Errorf("Required context file \"%s\" is unavailable.", rel)
		}
		instr := isInstruction(rel)
		size := len(f.content)
		var ex *Excerpt
		if instr || size <= maxMandatoryFileBytes {
			full := fullExcerpt(f)
			ex = &full
		} else {
			ex = candidateExcerpt(f, terms, maxMandatoryFileBytes, refs[rel])
		}
		if ex == nil {
			return Retrieved{}, fmt.Errorf("Required context file \"%s\" has no complete excerpt within the mandatory context budget.", rel)
		}
		if (instr && size > maxMandatoryFileBytes) || mandatoryBytes+len(ex.Content) > maxMandatoryBytes {
			return Retrieved{}, fmt.Errorf("Required context file \"%s\" exceeds the mandatory context budget.", rel)
		}
		if !instr && size > maxMandatoryFileBytes {
			omit(&omitted, rel, fmt.Sprintf("full file exceeds the excerpt budget; included lines %d-%d", ex.StartLine, ex.EndLine))
		}
		out.Mandatory = append(out.Mandatory, *ex)
		mandatoryBytes += len(ex.Content)
	}
	candidateBytes := 0
	for _, rel := range optional {
		if err := ctxActive(ctx, deadline); err != nil {
			return Retrieved{}, err
		}
		f, err := readFile(ctx, deadline, root, rel)
		if err != nil {
			return Retrieved{}, err
		}
		if f == nil {
			omit(&omitted, rel, "unreadable, binary, or outside workspace")
			continue
		}
		ex := candidateExcerpt(f, terms, maxCandidateExcerpt, 0)
		switch {
		case ex == nil:
			omit(&omitted, rel, "excerpt exceeds the per-file limit")
		case candidateBytes+len(ex.Content) > maxCandidateBytes:
			omit(&omitted, rel, "candidate context budget exhausted")
		default:
			out.Candidates = append(out.Candidates, *ex)
			candidateBytes += len(ex.Content)
		}
	}
	slices.SortFunc(omitted, func(a, b Omission) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Reason, b.Reason))
	})
	out.Omitted = omitted
	return out, nil
}

// ValidateRevisions fails when an excerpt's file is gone, now unsafe to
// read, or changed since RetrieveContext read it.
func ValidateRevisions(ctx context.Context, root string, excerpts []Excerpt) error {
	deadline := time.Now().Add(retrievalTimeout)
	if err := ctxActive(ctx, deadline); err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for _, ex := range excerpts {
		f, err := readFile(ctx, deadline, root, ex.Path)
		if err != nil {
			return err
		}
		if f == nil {
			return fmt.Errorf("Context excerpt \"%s\" is unavailable.", ex.Path)
		}
		if revision(f.bytes) != ex.Revision {
			return fmt.Errorf("Context excerpt \"%s\" changed after retrieval.", ex.Path)
		}
	}
	return nil
}
