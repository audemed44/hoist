// Package github is a small client for the parts of the GitHub REST API the
// release board needs: pull requests, workflow runs, branch comparisons,
// rebase merges and re-runs. GETs are conditional (ETags), so unchanged
// answers don't count against the rate limit, and only the fields Hoist
// reads are kept between requests.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	etag string
	data []byte // the decoded fields, re-encoded
	used time.Time
}

// maxCached bounds the ETag cache.
const maxCached = 400

// New returns a client for api.github.com with a token.
func New(token string) *Client {
	return NewWith("https://api.github.com", token, &http.Client{Timeout: 20 * time.Second})
}

// NewWith is New against another API root, e.g. a test server.
func NewWith(base, token string, h *http.Client) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), token: token, http: h, cache: map[string]cached{}}
}

// Error is an answer GitHub refused a request with.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitHub answered HTTP %d", e.Status)
	}
	return "GitHub: " + e.Message
}

// IsStatus reports whether err is a GitHub answer with that status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &e)
	msg := e.Message
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		msg = "rate limit reached; try again later"
	}
	if resp.StatusCode == http.StatusUnauthorized {
		msg = "the token was refused (expired, or revoked?)"
	}
	return &Error{Status: resp.StatusCode, Message: msg}
}

// get fetches path into v, conditionally when it was fetched before.
func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	prev, ok := c.cache[path]
	c.mu.Unlock()
	if ok {
		req.Header.Set("If-None-Match", prev.etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && ok {
		c.mu.Lock()
		prev.used = time.Now()
		c.cache[path] = prev
		c.mu.Unlock()
		return json.Unmarshal(prev.data, v)
	}
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v); err != nil {
		return fmt.Errorf("GitHub: %w", err)
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		if data, err := json.Marshal(v); err == nil {
			c.store(path, cached{etag: etag, data: data, used: time.Now()})
		}
	}
	return nil
}

func (c *Client) store(path string, e cached) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= maxCached {
		oldest := ""
		for k, v := range c.cache {
			if oldest == "" || v.used.Before(c.cache[oldest].used) {
				oldest = k
			}
		}
		delete(c.cache, oldest)
	}
	c.cache[path] = e
}

// send makes a request that changes something.
func (c *Client) send(ctx context.Context, method, path string, body, v any) error {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// segments escapes a branch name for a path, keeping its slashes.
func segments(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func repoPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// User is the login the token belongs to.
func (c *Client) User(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	err := c.get(ctx, "/user", &u)
	return u.Login, err
}

// DefaultBranch is a repository's default branch.
func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.get(ctx, repoPath(repo), &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

// Head is the commit a branch points to.
func (c *Client) Head(ctx context.Context, repo, branch string) (string, error) {
	var b struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.get(ctx, repoPath(repo)+"/branches/"+segments(branch), &b); err != nil {
		return "", err
	}
	return b.Commit.SHA, nil
}

type Commit struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"` // the first line
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
	URL     string    `json:"url"`
}

type Comparison struct {
	// Status is ahead, behind, diverged or identical: how head relates
	// to base.
	Status   string   `json:"status"`
	AheadBy  int      `json:"ahead_by"`
	BehindBy int      `json:"behind_by"`
	Commits  []Commit `json:"commits"` // in head, not in base; oldest first
	URL      string   `json:"url"`
}

// Compare lists what head has that base doesn't.
func (c *Client) Compare(ctx context.Context, repo, base, head string) (Comparison, error) {
	var raw struct {
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
		HTMLURL  string `json:"html_url"`
		Commits  []struct {
			SHA     string `json:"sha"`
			HTMLURL string `json:"html_url"`
			Commit  struct {
				Message string `json:"message"`
				Author  struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"commits"`
	}
	path := repoPath(repo) + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?per_page=50"
	if err := c.get(ctx, path, &raw); err != nil {
		return Comparison{}, err
	}
	out := Comparison{Status: raw.Status, AheadBy: raw.AheadBy, BehindBy: raw.BehindBy, URL: raw.HTMLURL, Commits: []Commit{}}
	for _, cm := range raw.Commits {
		first, _, _ := strings.Cut(cm.Commit.Message, "\n")
		out.Commits = append(out.Commits, Commit{SHA: cm.SHA, Message: first, Author: cm.Commit.Author.Name, Time: cm.Commit.Author.Date, URL: cm.HTMLURL})
	}
	return out, nil
}

type PullRequest struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	Draft   bool      `json:"draft"`
	Author  string    `json:"author"`
	Created time.Time `json:"created"`
	URL     string    `json:"url"`
	Branch  string    `json:"branch"`
	SHA     string    `json:"sha"`
	// SameRepo is set when the branch is in the repository itself (not
	// a fork), so it can be deleted after the merge.
	SameRepo bool `json:"same_repo"`
	// Mergeable and Rebaseable are nil while GitHub is still working
	// them out. MergeableState is clean, blocked, behind, dirty,
	// unstable, has_hooks or unknown.
	Mergeable      *bool  `json:"mergeable,omitempty"`
	Rebaseable     *bool  `json:"rebaseable,omitempty"`
	MergeableState string `json:"mergeable_state,omitempty"`
}

type rawPull struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Draft     bool      `json:"draft"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	Mergeable      *bool  `json:"mergeable"`
	Rebaseable     *bool  `json:"rebaseable"`
	MergeableState string `json:"mergeable_state"`
}

func (p rawPull) pull() PullRequest {
	out := PullRequest{
		Number: p.Number, Title: p.Title, Draft: p.Draft, Author: p.User.Login, Created: p.CreatedAt,
		URL: p.HTMLURL, Branch: p.Head.Ref, SHA: p.Head.SHA,
		Mergeable: p.Mergeable, Rebaseable: p.Rebaseable, MergeableState: p.MergeableState,
	}
	out.SameRepo = p.Head.Repo != nil && p.Head.Repo.FullName == p.Base.Repo.FullName
	return out
}

// Pulls lists a repository's open pull requests, newest first. The list
// doesn't say whether they can be merged; Pull does.
func (c *Client) Pulls(ctx context.Context, repo string) ([]PullRequest, error) {
	var raw []rawPull
	if err := c.get(ctx, repoPath(repo)+"/pulls?state=open&per_page=30", &raw); err != nil {
		return nil, err
	}
	out := make([]PullRequest, len(raw))
	for i, p := range raw {
		out[i] = p.pull()
	}
	return out, nil
}

// Pull fetches one pull request, with whether it can be merged.
func (c *Client) Pull(ctx context.Context, repo string, number int) (PullRequest, error) {
	var raw rawPull
	if err := c.get(ctx, repoPath(repo)+"/pulls/"+strconv.Itoa(number), &raw); err != nil {
		return PullRequest{}, err
	}
	return raw.pull(), nil
}

// Review sums up a pull request's reviews: approved, changes_requested,
// or "" (none, or only comments). Each reviewer's latest review counts.
func (c *Client) Review(ctx context.Context, repo string, number int) (string, error) {
	var raw []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := c.get(ctx, repoPath(repo)+"/pulls/"+strconv.Itoa(number)+"/reviews?per_page=100", &raw); err != nil {
		return "", err
	}
	latest := map[string]string{}
	for _, r := range raw {
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" || r.State == "DISMISSED" {
			latest[r.User.Login] = r.State
		}
	}
	out := ""
	for _, s := range latest {
		switch {
		case s == "CHANGES_REQUESTED":
			return "changes_requested", nil
		case s == "APPROVED":
			out = "approved"
		}
	}
	return out, nil
}

// Run is a workflow run.
type Run struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Workflow int64  `json:"workflow"`
	// Path is the workflow file, e.g. .github/workflows/docker.yml.
	Path    string `json:"path"`
	Event   string `json:"event"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha"`
	Attempt int    `json:"attempt"`
	// Status is queued, in_progress, completed (or waiting, requested,
	// pending); Conclusion, once completed, is success, failure,
	// cancelled, skipped, timed_out, action_required, neutral…
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	URL        string    `json:"url"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// Done reports whether the run has finished.
func (r Run) Done() bool { return r.Status == "completed" }

// Failed reports whether it finished without succeeding.
func (r Run) Failed() bool {
	switch r.Conclusion {
	case "failure", "timed_out", "startup_failure", "action_required", "cancelled":
		return r.Done()
	}
	return false
}

type rawRuns struct {
	Runs []struct {
		ID         int64     `json:"id"`
		Name       string    `json:"name"`
		WorkflowID int64     `json:"workflow_id"`
		Path       string    `json:"path"`
		Event      string    `json:"event"`
		HeadBranch string    `json:"head_branch"`
		HeadSHA    string    `json:"head_sha"`
		RunAttempt int       `json:"run_attempt"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		HTMLURL    string    `json:"html_url"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	} `json:"workflow_runs"`
}

func (r rawRuns) runs() []Run {
	out := make([]Run, 0, len(r.Runs))
	for _, x := range r.Runs {
		path, _, _ := strings.Cut(x.Path, "@") // reusable workflows say path@ref
		out = append(out, Run{
			ID: x.ID, Name: x.Name, Workflow: x.WorkflowID, Path: path, Event: x.Event, Branch: x.HeadBranch,
			SHA: x.HeadSHA, Attempt: x.RunAttempt, Status: x.Status, Conclusion: x.Conclusion, URL: x.HTMLURL,
			Created: x.CreatedAt, Updated: x.UpdatedAt,
		})
	}
	return out
}

// RunsFor lists the workflow runs for a commit, newest first.
func (c *Client) RunsFor(ctx context.Context, repo, sha string) ([]Run, error) {
	var raw rawRuns
	if err := c.get(ctx, repoPath(repo)+"/actions/runs?per_page=30&head_sha="+url.QueryEscape(sha), &raw); err != nil {
		return nil, err
	}
	return raw.runs(), nil
}

// WorkflowRuns lists one workflow's runs on a branch, newest first;
// sha, if set, narrows them to that commit.
func (c *Client) WorkflowRuns(ctx context.Context, repo, workflow, branch, sha string) ([]Run, error) {
	q := url.Values{"per_page": {"5"}, "branch": {branch}}
	if sha != "" {
		q.Set("head_sha", sha)
	}
	var raw rawRuns
	path := repoPath(repo) + "/actions/workflows/" + url.PathEscape(workflow) + "/runs?" + q.Encode()
	if err := c.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	return raw.runs(), nil
}

// Merge rebase-merges a pull request whose head is still sha, and returns
// the new head of the base branch.
func (c *Client) Merge(ctx context.Context, repo string, number int, sha string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	body := map[string]string{"merge_method": "rebase", "sha": sha}
	err := c.send(ctx, http.MethodPut, repoPath(repo)+"/pulls/"+strconv.Itoa(number)+"/merge", body, &out)
	return out.SHA, err
}

// DeleteBranch deletes a branch; one already gone is fine.
func (c *Client) DeleteBranch(ctx context.Context, repo, branch string) error {
	err := c.send(ctx, http.MethodDelete, repoPath(repo)+"/git/refs/heads/"+segments(branch), nil, nil)
	if IsStatus(err, http.StatusUnprocessableEntity) || IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// RerunFailed re-runs a workflow run's failed jobs.
func (c *Client) RerunFailed(ctx context.Context, repo string, run int64) error {
	return c.send(ctx, http.MethodPost, repoPath(repo)+"/actions/runs/"+strconv.FormatInt(run, 10)+"/rerun-failed-jobs", nil, nil)
}
