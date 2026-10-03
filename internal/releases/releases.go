// Package releases builds the release board: for each of your own apps
// that Hoist deploys, the way from pull request to running container. Apps
// are found from the running containers' OCI labels (image.source names
// the GitHub repository, image.revision the commit), so there's nothing to
// configure; GitHub is read when the board is looked at, not in the
// background.
package releases

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/github"
	"github.com/audemed44/hoist/internal/registry"
)

// App states, from what most needs doing.
const (
	BuildFailed = "build-failed" // the image build of the latest commit failed
	Ready       = "ready"        // a new image is published but not deployed
	Building    = "building"     // the latest commit's image is being built
	NotBuilt    = "not-built"    // the branch moved on without a new image
	Deployed    = "deployed"     // running the latest commit's image
)

// Pull request states.
const (
	PRDraft     = "draft"
	PRConflict  = "conflict"   // can't be rebased cleanly
	PRFailing   = "ci-failing" // a check failed
	PRRunning   = "ci-running"
	PRChecking  = "checking" // GitHub is still working out mergeability
	PRBlocked   = "blocked"  // GitHub's rules don't allow the merge yet
	PRChanges   = "changes-requested"
	PRReady     = "ready"
	CIPassing   = "passing"
	CIFailing   = "failing"
	CIPending   = "pending"
	CINone      = "none"
	maxCommits  = 20
	prWorkers   = 4
	appWorkers  = 4
	callTimeout = 45 * time.Second
)

type Board struct {
	// Configured is false without a GitHub token.
	Configured bool      `json:"configured"`
	CheckedAt  time.Time `json:"checked_at"`
	Owners     []string  `json:"owners"`
	Apps       []App     `json:"apps"`
	// Error is a problem with the whole board, e.g. the token was refused.
	Error string `json:"error,omitempty"`
}

// Running is what the app's containers run.
type Running struct {
	Revision string `json:"revision,omitempty"`
	Digest   string `json:"digest,omitempty"`
	// Since is when the oldest of its containers was created.
	Since time.Time `json:"since"`
}

type App struct {
	// ID is repo@stack: an app deployed in two stacks gets two cards.
	ID       string   `json:"id"`
	Repo     string   `json:"repo"` // owner/name
	URL      string   `json:"url"`
	Stack    string   `json:"stack"`
	Services []string `json:"services"`
	Image    string   `json:"image"`
	Running  Running  `json:"running"`
	Branch   string   `json:"branch"`
	Head     string   `json:"head"`
	// Behind is how many commits the branch has that the running image
	// doesn't; Commits are the newest of them, newest first.
	Behind     int             `json:"behind"`
	Diverged   bool            `json:"diverged,omitempty"`
	Commits    []github.Commit `json:"commits"`
	CompareURL string          `json:"compare_url,omitempty"`
	// Build is the latest run of the image workflow on the branch.
	Build *github.Run `json:"build,omitempty"`
	// Latest is the registry digest of the image's tag now.
	Latest string   `json:"latest,omitempty"`
	State  string   `json:"state"`
	PRs    []PR     `json:"prs"`
	Errors []string `json:"errors"`
}

type PR struct {
	github.PullRequest
	Review string `json:"review,omitempty"`
	CI     string `json:"ci"`
	// Runs are the latest run of each workflow for the head commit.
	Runs  []github.Run `json:"runs"`
	State string       `json:"state"`
}

// FailedRuns are the runs whose failed jobs can be re-run.
func (p PR) FailedRuns() []int64 {
	var out []int64
	for _, r := range p.Runs {
		if r.Failed() {
			out = append(out, r.ID)
		}
	}
	return out
}

// Source reads the board from Docker, the registries and GitHub.
type Source struct {
	GitHub   *github.Client // nil without a token
	Registry *registry.Client
	Docker   *docker.Client
	Config   *config.Config

	build    sync.Mutex // one build at a time; the others use its result
	mu       sync.Mutex
	board    *Board
	branches map[string]string // repo → default branch
	login    string
}

// Get returns the board, building it when the cached one is older than
// maxAge.
func (s *Source) Get(ctx context.Context, maxAge time.Duration) *Board {
	if s.GitHub == nil {
		return &Board{Apps: []App{}, Owners: []string{}}
	}
	if b := s.cached(maxAge); b != nil {
		return b
	}
	s.build.Lock()
	defer s.build.Unlock()
	if b := s.cached(maxAge); b != nil {
		return b
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	b := s.read(ctx)
	s.mu.Lock()
	s.board = b
	s.mu.Unlock()
	return b
}

// Cached returns the last board, however old, or nil.
func (s *Source) Cached() *Board { return s.cached(-1) }

func (s *Source) cached(maxAge time.Duration) *Board {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.board == nil || (maxAge >= 0 && time.Since(s.board.CheckedAt) > maxAge) {
		return nil
	}
	return s.board
}

// Forget drops the cached board, after something changed it.
func (s *Source) Forget() {
	s.mu.Lock()
	s.board = nil
	s.mu.Unlock()
}

func (s *Source) read(ctx context.Context) *Board {
	b := &Board{Configured: true, CheckedAt: time.Now().UTC(), Apps: []App{}, Owners: s.Config.Releases.Owners}
	if len(b.Owners) == 0 {
		login, err := s.user(ctx)
		if err != nil {
			b.Error = err.Error()
			return b
		}
		b.Owners = []string{login}
	}
	apps, err := s.discover(ctx, b.Owners)
	if err != nil {
		b.Error = err.Error()
		return b
	}
	sem := make(chan struct{}, appWorkers)
	var wg sync.WaitGroup
	for i := range apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.fill(ctx, &apps[i])
		}()
	}
	wg.Wait()
	b.Apps = apps
	for _, a := range apps {
		for _, e := range a.Errors {
			if strings.Contains(e, "token was refused") {
				b.Error = e
			}
		}
	}
	return b
}

func (s *Source) user(ctx context.Context) (string, error) {
	s.mu.Lock()
	login := s.login
	s.mu.Unlock()
	if login != "" {
		return login, nil
	}
	login, err := s.GitHub.User(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.login = login
	s.mu.Unlock()
	return login, nil
}

// RepoOf reads owner/name from an image.source label pointing at GitHub.
func RepoOf(source string) string {
	rest, ok := strings.CutPrefix(source, "https://github.com/")
	if !ok {
		return ""
	}
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// discover finds the apps: containers of Hoist's stacks whose image says
// it was built from a repository of one of the owners.
func (s *Source) discover(ctx context.Context, owners []string) ([]App, error) {
	containers, err := s.Docker.Containers(ctx)
	if err != nil {
		return nil, err
	}
	stacks := map[string]string{} // project → stack
	for _, st := range s.Config.List() {
		stacks[st.Project] = st.Name
	}
	byID := map[string]*App{}
	var order []string
	for _, c := range containers {
		stack, ok := stacks[c.Project]
		repo := RepoOf(c.Source)
		if !ok || c.OneOff || repo == "" || slices.Contains(s.Config.Releases.Ignore, repo) {
			continue
		}
		owner, _, _ := strings.Cut(repo, "/")
		if !slices.ContainsFunc(owners, func(o string) bool { return strings.EqualFold(o, owner) }) {
			continue
		}
		id := repo + "@" + stack
		a := byID[id]
		if a == nil {
			a = &App{ID: id, Repo: repo, URL: "https://github.com/" + repo, Stack: stack, Image: c.Image,
				Commits: []github.Commit{}, PRs: []PR{}, Errors: []string{}, Services: []string{}}
			a.Running.Revision = c.Revision
			a.Running.Since = time.Unix(c.Created, 0).UTC()
			if img, err := s.Docker.Image(ctx, c.ImageID); err == nil {
				a.Image, a.Running.Digest = registry.DigestFor(c.Image, img.RepoDigests)
			}
			byID[id] = a
			order = append(order, id)
		}
		if !slices.Contains(a.Services, c.Service) {
			a.Services = append(a.Services, c.Service)
		}
		if t := time.Unix(c.Created, 0).UTC(); t.Before(a.Running.Since) {
			a.Running.Since = t
		}
	}
	sort.Strings(order)
	out := make([]App, 0, len(order))
	for _, id := range order {
		sort.Strings(byID[id].Services)
		out = append(out, *byID[id])
	}
	return out, nil
}

// fill reads one app's branch, image build, registry digest and pull
// requests.
func (s *Source) fill(ctx context.Context, a *App) {
	var mu sync.Mutex
	fail := func(what string, err error) {
		mu.Lock()
		a.Errors = append(a.Errors, what+": "+err.Error())
		mu.Unlock()
	}
	branch, err := s.defaultBranch(ctx, a.Repo)
	if err != nil {
		fail("repository", err)
		a.State = Deployed
		if a.Running.Revision == "" {
			a.State = NotBuilt
		}
		return
	}
	a.Branch = branch
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		head, err := s.GitHub.Head(ctx, a.Repo, branch)
		if err != nil {
			fail("branch", err)
			return
		}
		a.Head = head
		if a.Running.Revision == "" || a.Running.Revision == a.Head {
			return
		}
		cmp, err := s.GitHub.Compare(ctx, a.Repo, a.Running.Revision, a.Head)
		if err != nil {
			fail("commits", err)
			return
		}
		a.Behind, a.Diverged, a.CompareURL = cmp.AheadBy, cmp.BehindBy > 0, cmp.URL
		slices.Reverse(cmp.Commits)
		a.Commits = cmp.Commits[:min(maxCommits, len(cmp.Commits))]
	}()
	go func() {
		defer wg.Done()
		runs, err := s.GitHub.WorkflowRuns(ctx, a.Repo, s.Config.Releases.Workflow, branch, "")
		switch {
		case github.IsStatus(err, http.StatusNotFound):
			// No such workflow.
		case err != nil:
			fail("image build", err)
		case len(runs) > 0:
			a.Build = &runs[0]
		}
		if ref, err := registry.Parse(a.Image); err == nil && ref.Tag != "" && ref.Digest == "" {
			if d, err := s.Registry.Digest(ctx, ref); err == nil {
				a.Latest = d
			} else {
				fail("registry", err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		prs, err := s.pulls(ctx, a.Repo)
		if err != nil {
			fail("pull requests", err)
			return
		}
		a.PRs = prs
	}()
	wg.Wait()
	a.State = AppState(*a)
}

func (s *Source) defaultBranch(ctx context.Context, repo string) (string, error) {
	s.mu.Lock()
	b := s.branches[repo]
	s.mu.Unlock()
	if b != "" {
		return b, nil
	}
	b, err := s.GitHub.DefaultBranch(ctx, repo)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.branches == nil {
		s.branches = map[string]string{}
	}
	s.branches[repo] = b
	s.mu.Unlock()
	return b, nil
}

func (s *Source) pulls(ctx context.Context, repo string) ([]PR, error) {
	list, err := s.GitHub.Pulls(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]PR, len(list))
	errs := make([]error, len(list))
	sem := make(chan struct{}, prWorkers)
	var wg sync.WaitGroup
	for i, p := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = s.pull(ctx, repo, p)
		}()
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

// pull reads what the list leaves out: mergeability, checks and reviews.
func (s *Source) pull(ctx context.Context, repo string, p github.PullRequest) (PR, error) {
	out := PR{PullRequest: p, Runs: []github.Run{}}
	full, err := s.GitHub.Pull(ctx, repo, p.Number)
	if err != nil {
		return out, err
	}
	out.PullRequest = full
	runs, err := s.GitHub.RunsFor(ctx, repo, full.SHA)
	if err != nil {
		return out, err
	}
	out.Runs = LatestPerWorkflow(runs)
	out.CI = CI(out.Runs)
	if out.Review, err = s.GitHub.Review(ctx, repo, p.Number); err != nil {
		return out, err
	}
	out.State = PRState(out)
	return out, nil
}

// LatestPerWorkflow keeps the newest run of each workflow.
func LatestPerWorkflow(runs []github.Run) []github.Run {
	latest := map[int64]github.Run{}
	for _, r := range runs {
		if cur, ok := latest[r.Workflow]; !ok || r.Created.After(cur.Created) {
			latest[r.Workflow] = r
		}
	}
	out := make([]github.Run, 0, len(latest))
	for _, r := range latest {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CI sums up the runs of a commit.
func CI(runs []github.Run) string {
	if len(runs) == 0 {
		return CINone
	}
	state := CIPassing
	for _, r := range runs {
		switch {
		case r.Failed():
			return CIFailing
		case !r.Done():
			state = CIPending
		}
	}
	return state
}

// PRState is what a pull request is waiting for.
func PRState(p PR) string {
	switch {
	case p.Draft:
		return PRDraft
	case (p.Rebaseable != nil && !*p.Rebaseable) || p.MergeableState == "dirty":
		return PRConflict
	case p.CI == CIFailing:
		return PRFailing
	case p.CI == CIPending:
		return PRRunning
	case p.Mergeable == nil:
		return PRChecking
	case p.Review == "changes_requested":
		return PRChanges
	case p.MergeableState == "blocked":
		return PRBlocked
	}
	return PRReady
}

// AppState is where an app stands between its branch and what runs.
func AppState(a App) string {
	current := a.Running.Revision != "" && a.Running.Revision == a.Head
	if b := a.Build; b != nil && a.Head != "" && b.SHA == a.Head && !current {
		switch {
		case !b.Done():
			return Building
		case b.Failed():
			return BuildFailed
		case b.Conclusion == "success":
			return Ready
		}
	}
	if a.Latest != "" && a.Running.Digest != "" && a.Latest != a.Running.Digest {
		return Ready
	}
	if current || a.Running.Revision == "" || a.Head == "" {
		return Deployed
	}
	return NotBuilt
}

// Summary counts what the board asks of you.
type Summary struct {
	PRs     int `json:"prs"`      // open, not drafts
	Ready   int `json:"ready"`    // apps with an image waiting to deploy
	Failing int `json:"failing"`  // PRs with failing checks, and failed builds
	ToMerge int `json:"to_merge"` // PRs ready to merge
}

func (b *Board) Summary() Summary {
	var s Summary
	for _, a := range b.Apps {
		switch a.State {
		case Ready:
			s.Ready++
		case BuildFailed:
			s.Failing++
		}
		for _, p := range a.PRs {
			if p.Draft {
				continue
			}
			s.PRs++
			switch p.State {
			case PRFailing:
				s.Failing++
			case PRReady:
				s.ToMerge++
			}
		}
	}
	return s
}

// Find returns an app by repository (and stack, when it runs in more than
// one).
func (b *Board) Find(repo, stack string) (*App, bool) {
	for i := range b.Apps {
		a := &b.Apps[i]
		if strings.EqualFold(a.Repo, repo) && (stack == "" || a.Stack == stack) {
			return a, true
		}
	}
	return nil, false
}
