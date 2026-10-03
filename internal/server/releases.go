package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/github"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/releases"
)

// boardAge is how long the release board is reused; refresh asks again
// sooner, but not more often than refreshAge.
const (
	boardAge   = 2 * time.Minute
	refreshAge = 10 * time.Second
)

// releaseApp is an app on the board with what Hoist knows of its stack.
type releaseApp struct {
	releases.App
	// Pinned is set while its stack is rolled back.
	Pinned bool `json:"pinned,omitempty"`
	// Active is its stack's running deploy; Last the latest finished one.
	Active *jobs.Job `json:"active,omitempty"`
	Last   *jobs.Job `json:"last,omitempty"`
}

type releasesResponse struct {
	Configured bool             `json:"configured"`
	CheckedAt  time.Time        `json:"checked_at"`
	Owners     []string         `json:"owners"`
	Error      string           `json:"error,omitempty"`
	Workflow   string           `json:"workflow"`
	Apps       []releaseApp     `json:"apps"`
	Summary    releases.Summary `json:"summary"`
}

func (s *Server) board(r *http.Request, maxAge time.Duration) (*releases.Board, releasesResponse) {
	b := s.Releases.Get(r.Context(), maxAge)
	resp := releasesResponse{
		Configured: b.Configured, CheckedAt: b.CheckedAt, Owners: b.Owners, Error: b.Error,
		Workflow: s.Config.Releases.Workflow, Apps: []releaseApp{}, Summary: b.Summary(),
	}
	for _, a := range b.Apps {
		ra := releaseApp{App: a, Active: s.active(a.Stack), Last: s.Jobs.Latest(a.Stack)}
		if st, ok := s.Config.Stack(a.Stack); ok {
			ra.Pinned = st.Pin != nil
		}
		resp.Apps = append(resp.Apps, ra)
	}
	return b, resp
}

func (s *Server) getReleases(w http.ResponseWriter, r *http.Request) {
	age := boardAge
	if r.URL.Query().Get("refresh") != "" {
		age = refreshAge
	}
	_, resp := s.board(r, age)
	writeJSON(w, http.StatusOK, resp)
}

// releaseTarget names an app on the board, and optionally one of its pull
// requests.
type releaseTarget struct {
	Repo   string `json:"repo"`
	Stack  string `json:"stack"`
	Number int    `json:"number"`
	// Force merges a pull request whose checks aren't green.
	Force bool `json:"force"`
}

// app finds a target's app on a fresh board.
func (s *Server) app(r *http.Request, t releaseTarget) (*releases.App, int, error) {
	if s.Releases.GitHub == nil {
		return nil, http.StatusConflict, errors.New("set HOIST_GITHUB_TOKEN to use the release board")
	}
	b := s.Releases.Get(r.Context(), refreshAge)
	if b.Error != "" {
		return nil, http.StatusBadGateway, errors.New(b.Error)
	}
	a, ok := b.Find(t.Repo, t.Stack)
	if !ok {
		return nil, http.StatusNotFound, fmt.Errorf("%s isn't on the release board", t.Repo)
	}
	return a, http.StatusOK, nil
}

func findPR(a *releases.App, number int) (*releases.PR, bool) {
	for i := range a.PRs {
		if a.PRs[i].Number == number {
			return &a.PRs[i], true
		}
	}
	return nil, false
}

// mergeResult is a merged pull request: the branch's new head, and whether
// its branch was deleted.
type mergeResult struct {
	SHA         string `json:"sha"`
	Deleted     bool   `json:"deleted"`
	DeleteError string `json:"delete_error,omitempty"`
}

// merge rebase-merges a pull request and deletes its branch. It refuses
// one GitHub can't rebase, or a draft; one whose checks aren't green only
// with force.
func (s *Server) merge(ctx context.Context, a *releases.App, number int, force bool, trigger string) (*mergeResult, int, error) {
	p, ok := findPR(a, number)
	if !ok {
		return nil, http.StatusNotFound, fmt.Errorf("%s has no open pull request #%d", a.Repo, number)
	}
	ev := audit.Event{Stack: a.Stack, Services: a.Services, Action: audit.ReleaseMerge, Trigger: trigger,
		Detail: fmt.Sprintf("%s#%d: %s", a.Repo, p.Number, p.Title)}
	fail := func(status int, err error) (*mergeResult, int, error) {
		ev.Result, ev.Error = audit.Failed, err.Error()
		s.record(ev)
		return nil, status, err
	}
	switch p.State {
	case releases.PRReady:
	case releases.PRDraft:
		return fail(http.StatusConflict, errors.New("it's a draft"))
	case releases.PRConflict:
		return fail(http.StatusConflict, errors.New("GitHub can't rebase it cleanly; resolve it on GitHub: "+p.URL))
	default:
		if !force {
			return nil, http.StatusConflict, fmt.Errorf("it isn't ready to merge (%s)", p.State)
		}
		ev.Detail += " (merged without green checks)"
	}
	sha, err := s.Releases.GitHub.Merge(ctx, a.Repo, p.Number, p.SHA)
	s.Releases.Forget()
	if err != nil {
		status := http.StatusBadGateway
		if github.IsStatus(err, http.StatusMethodNotAllowed) || github.IsStatus(err, http.StatusConflict) {
			status = http.StatusConflict
		}
		return fail(status, err)
	}
	res := &mergeResult{SHA: sha}
	ev.Commit = shortSHA(sha)
	if p.SameRepo {
		if err := s.Releases.GitHub.DeleteBranch(ctx, a.Repo, p.Branch); err != nil {
			res.DeleteError = err.Error()
			ev.Error = "merged, but the branch wasn't deleted: " + err.Error()
		} else {
			res.Deleted = true
		}
	}
	s.record(ev)
	return res, http.StatusOK, nil
}

func shortSHA(sha string) string { return sha[:min(7, len(sha))] }

func (s *Server) postMerge(w http.ResponseWriter, r *http.Request) {
	var t releaseTarget
	if !readJSON(w, r, 4<<10, &t) {
		return
	}
	a, status, err := s.app(r, t)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	res, status, err := s.merge(r.Context(), a, t.Number, t.Force, releaseTrigger(r))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// rerun re-runs the failed jobs of a pull request's checks, or with
// number 0, of the app's failed image build.
func (s *Server) rerun(ctx context.Context, a *releases.App, number int, trigger string) (int, error) {
	var runs []int64
	what := "the image build"
	if number == 0 {
		if a.Build != nil && a.Build.Failed() {
			runs = []int64{a.Build.ID}
		}
	} else {
		p, ok := findPR(a, number)
		if !ok {
			return http.StatusNotFound, fmt.Errorf("%s has no open pull request #%d", a.Repo, number)
		}
		runs = p.FailedRuns()
		what = fmt.Sprintf("#%d's checks", number)
	}
	if len(runs) == 0 {
		return http.StatusConflict, errors.New("nothing has failed")
	}
	ev := audit.Event{Stack: a.Stack, Services: a.Services, Action: audit.ReleaseRerun, Trigger: trigger,
		Detail: fmt.Sprintf("%s: %s", a.Repo, what)}
	var errs []error
	for _, id := range runs {
		errs = append(errs, s.Releases.GitHub.RerunFailed(ctx, a.Repo, id))
	}
	s.Releases.Forget()
	if err := errors.Join(errs...); err != nil {
		ev.Result, ev.Error = audit.Failed, err.Error()
		s.record(ev)
		return http.StatusBadGateway, err
	}
	s.record(ev)
	return http.StatusAccepted, nil
}

func (s *Server) postRerun(w http.ResponseWriter, r *http.Request) {
	var t releaseTarget
	if !readJSON(w, r, 4<<10, &t) {
		return
	}
	a, status, err := s.app(r, t)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if status, err := s.rerun(r.Context(), a, t.Number, releaseTrigger(r)); err != nil {
		writeError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// deployApp pulls and redeploys the services that run an app's image.
func (s *Server) deployApp(a *releases.App, trigger string) (*jobs.Job, int, error) {
	st, ok := s.Config.Stack(a.Stack)
	if !ok {
		return nil, http.StatusNotFound, fmt.Errorf("no stack %q", a.Stack)
	}
	if st.Pin != nil {
		return nil, http.StatusConflict, errors.New(st.Name + " is rolled back; resume :latest on its page to deploy new images")
	}
	job, status, err := s.startDeploy(st, a.Services, trigger, "")
	s.Releases.Forget()
	return job, status, err
}

func (s *Server) postReleaseDeploy(w http.ResponseWriter, r *http.Request) {
	var t releaseTarget
	if !readJSON(w, r, 4<<10, &t) {
		return
	}
	a, status, err := s.app(r, t)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	job, status, err := s.deployApp(a, releaseTrigger(r))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, status, job)
}

// releaseTrigger is "releases" for the board in a browser; scripts with
// the token stay "api".
func releaseTrigger(r *http.Request) string {
	if t := triggerOf(r); t != "ui" {
		return t
	}
	return "releases"
}
