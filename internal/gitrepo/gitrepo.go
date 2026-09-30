// Package gitrepo runs the handful of git commands Hoist needs: status,
// fetch, commit and push of a single file, pull, and a file's history.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is a work tree. Commands in one repo are serialised, since several
// stacks can live in the same repo.
type Repo struct {
	Root string

	mu        sync.Mutex
	fetchedAt time.Time
	fetchErr  error
}

var (
	reposMu sync.Mutex
	repos   = map[string]*Repo{}
)

// Open finds the repo holding dir. It returns nil, nil when dir isn't in one.
func Open(ctx context.Context, dir string) (*Repo, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return nil, nil
		}
		return nil, err
	}
	root := strings.TrimSpace(out)
	reposMu.Lock()
	defer reposMu.Unlock()
	if r, ok := repos[root]; ok {
		return r, nil
	}
	r := &Repo{Root: root}
	repos[root] = r
	return r, nil
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	return runEnv(ctx, dir, nil, args...)
}

func runEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never wait for a password prompt; fail instead.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", args[0], redact(msg))
	}
	return stdout.String(), nil
}

// redact hides credentials in remote URLs that git echoes in errors.
func redact(s string) string {
	for _, scheme := range []string{"https://", "http://"} {
		rest := s
		var b strings.Builder
		for {
			i := strings.Index(rest, scheme)
			if i < 0 {
				b.WriteString(rest)
				break
			}
			b.WriteString(rest[:i+len(scheme)])
			rest = rest[i+len(scheme):]
			end := strings.IndexAny(rest, "/ \n")
			if end < 0 {
				end = len(rest)
			}
			if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
				b.WriteString("***")
				rest = rest[at:]
			}
		}
		s = b.String()
	}
	return s
}

// Rel is path relative to the repo root, with forward slashes.
func (r *Repo) Rel(path string) (string, error) {
	rel, err := filepath.Rel(r.Root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is outside the repo", path)
	}
	return filepath.ToSlash(rel), nil
}

type Status struct {
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	// Modified is true when the file differs from HEAD (edited outside
	// Hoist, or a commit failed).
	Modified bool `json:"modified"`
	// Untracked is true when the file isn't in git at all.
	Untracked  bool       `json:"untracked"`
	FetchedAt  *time.Time `json:"fetched_at,omitempty"`
	FetchError string     `json:"fetch_error,omitempty"`
}

// Status describes the branch and one file. It fetches first when the last
// fetch is older than maxAge (0 never fetches).
func (r *Repo) Status(ctx context.Context, file string, maxAge time.Duration) (Status, error) {
	if maxAge > 0 {
		r.fetchIfStale(ctx, maxAge)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var st Status
	rel, err := r.Rel(file)
	if err != nil {
		return st, err
	}
	out, err := run(ctx, r.Root, "status", "--porcelain=v2", "--branch", "--", rel)
	if err != nil {
		return st, err
	}
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "# branch.oid "):
			st.Head = strings.TrimPrefix(l, "# branch.oid ")
			if len(st.Head) > 7 {
				st.Head = st.Head[:7]
			}
		case strings.HasPrefix(l, "# branch.head "):
			st.Branch = strings.TrimPrefix(l, "# branch.head ")
		case strings.HasPrefix(l, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(l, "# branch.upstream ")
		case strings.HasPrefix(l, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(l, "# branch.ab "))
			if len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(l, "1 "), strings.HasPrefix(l, "2 "), strings.HasPrefix(l, "u "):
			st.Modified = true
		case strings.HasPrefix(l, "? "):
			st.Untracked = true
		}
	}
	if !r.fetchedAt.IsZero() {
		t := r.fetchedAt
		st.FetchedAt = &t
	}
	if r.fetchErr != nil {
		st.FetchError = r.fetchErr.Error()
	}
	return st, nil
}

func (r *Repo) fetchIfStale(ctx context.Context, maxAge time.Duration) {
	r.mu.Lock()
	stale := time.Since(r.fetchedAt) > maxAge
	r.mu.Unlock()
	if stale {
		_ = r.Fetch(ctx)
	}
}

// Fetch updates the remote-tracking branch, so Status can say "behind".
func (r *Repo) Fetch(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := run(ctx, r.Root, "fetch", "--quiet", "--no-tags")
	r.fetchedAt, r.fetchErr = time.Now(), err
	return err
}

type Author struct{ Name, Email string }

// ErrNothingToCommit means the file already matches HEAD.
var ErrNothingToCommit = errors.New("nothing to commit")

// Commit records one file with message, as author, and returns the short hash.
// Only that file goes in, whatever else is staged or modified.
func (r *Repo) Commit(ctx context.Context, file, message string, author Author) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, err := r.Rel(file)
	if err != nil {
		return "", err
	}
	// A tracked file is staged with -u, which works even when .gitignore
	// covers its folder (as after Track).
	add := []string{"add", "--", rel}
	if _, err := run(ctx, r.Root, "ls-files", "--error-unmatch", "--", rel); err == nil {
		add = []string{"add", "--update", "--", rel}
	}
	if _, err := run(ctx, r.Root, add...); err != nil {
		return "", err
	}
	if _, err := run(ctx, r.Root, "diff", "--cached", "--quiet", "--", rel); err == nil {
		return "", ErrNothingToCommit
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + author.Name, "GIT_COMMITTER_EMAIL=" + author.Email,
	}
	if _, err := runEnv(ctx, r.Root, env, "commit", "--quiet", "--no-verify", "-m", message, "--", rel); err != nil {
		return "", err
	}
	out, err := run(ctx, r.Root, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out), err
}

// Push sends the current branch to its upstream.
func (r *Repo) Push(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err := run(ctx, r.Root, "push", "--quiet")
	return err
}

// Pull fast-forwards the branch to its upstream. It refuses to merge.
func (r *Repo) Pull(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err := run(ctx, r.Root, "pull", "--ff-only", "--quiet", "--no-rebase")
	if err == nil {
		r.fetchedAt, r.fetchErr = time.Now(), nil
	}
	return err
}

type Commit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
	Subject string    `json:"subject"`
	// Path is where the file was in this commit (it may have moved since).
	Path string `json:"path"`
}

// Log lists the commits that touched file, newest first, following renames.
func (r *Repo) Log(ctx context.Context, file string, limit int) ([]Commit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, err := r.Rel(file)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, r.Root, "log", "--follow", "-n", strconv.Itoa(limit),
		"--format=%x1e%H%x1f%h%x1f%an%x1f%at%x1f%s", "--name-only", "--", rel)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return []Commit{}, nil
		}
		return nil, err
	}
	commits := []Commit{}
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		header, names, _ := strings.Cut(rec, "\n")
		f := strings.Split(header, "\x1f")
		if len(f) != 5 {
			continue
		}
		ts, _ := strconv.ParseInt(f[3], 10, 64)
		path := rel
		for _, n := range strings.Split(names, "\n") {
			if n = strings.TrimSpace(n); n != "" {
				path = n
				break
			}
		}
		commits = append(commits, Commit{
			Hash: f[0], Short: f[1], Author: f[2], Time: time.Unix(ts, 0).UTC(), Subject: f[4], Path: path,
		})
	}
	return commits, nil
}

// Show returns a file's content at a commit.
func (r *Repo) Show(ctx context.Context, hash, path string) ([]byte, error) {
	if !isHash(hash) || strings.Contains(path, "..") || strings.HasPrefix(path, "-") {
		return nil, errors.New("invalid commit or path")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := run(ctx, r.Root, "show", hash+":"+path)
	return []byte(out), err
}

func isHash(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Committed returns a file as it is in HEAD.
// Ignored reports whether .gitignore keeps file out of git (tracked files
// never are).
func (r *Repo) Ignored(ctx context.Context, file string) (bool, error) {
	rel, err := r.Rel(file)
	if err != nil {
		return false, err
	}
	out, err := run(ctx, r.Root, "check-ignore", "--", rel)
	if strings.TrimSpace(out) != "" {
		return true, nil
	}
	if err != nil && !strings.HasSuffix(err.Error(), "exit status 1") { // 1: not ignored
		return false, err
	}
	return false, nil
}

// Track stages file even though .gitignore excludes it. Once tracked, later
// commits of it work as usual.
func (r *Repo) Track(ctx context.Context, file string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, err := r.Rel(file)
	if err != nil {
		return err
	}
	_, err = run(ctx, r.Root, "add", "--force", "--", rel)
	return err
}

func (r *Repo) Committed(ctx context.Context, file string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, err := r.Rel(file)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, r.Root, "show", "HEAD:"+rel)
	return []byte(out), err
}

// Restore puts a file back as it is in HEAD, throwing away local edits.
func (r *Repo) Restore(ctx context.Context, file string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, err := r.Rel(file)
	if err != nil {
		return err
	}
	_, err = run(ctx, r.Root, "checkout", "HEAD", "--", rel)
	return err
}
