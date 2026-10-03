package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/releases"
)

// A ship is "merge and deploy when ready": merge a pull request, wait for
// the image build of the merged commit, then deploy the app. You start it,
// it isn't a standing rule, and it can be cancelled until the deploy
// starts. Ships live in memory only.

// Ship states.
const (
	ShipBuilding  = "building"  // merged; waiting for the image build
	ShipDeploying = "deploying" // the deploy is running
	ShipDone      = "done"
	ShipFailed    = "failed"
	ShipCancelled = "cancelled"
)

var (
	// shipPoll is how often the image build is asked about.
	shipPoll = 15 * time.Second
	// shipStart is how long to wait for the build to show up at all, and
	// shipTimeout for the whole thing.
	shipStart   = 5 * time.Minute
	shipTimeout = 45 * time.Minute
	// shipKeep is how long a finished ship stays on the board.
	shipKeep = time.Hour
)

type Ship struct {
	ID       string     `json:"id"`
	Repo     string     `json:"repo"`
	Stack    string     `json:"stack"`
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	SHA      string     `json:"sha"`
	State    string     `json:"state"`
	Message  string     `json:"message"`
	BuildURL string     `json:"build_url,omitempty"`
	Job      string     `json:"job,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`

	trigger string
	cancel  context.CancelFunc
}

type shipyard struct {
	mu    sync.Mutex
	ships map[string]*Ship
}

// list returns the ships running and recently finished, oldest first,
// dropping older ones.
func (y *shipyard) list() []Ship {
	y.mu.Lock()
	defer y.mu.Unlock()
	out := []Ship{}
	for id, sh := range y.ships {
		if sh.Finished != nil && time.Since(*sh.Finished) > shipKeep {
			delete(y.ships, id)
			continue
		}
		out = append(out, *sh)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

func (y *shipyard) update(id string, fn func(*Ship)) Ship {
	y.mu.Lock()
	defer y.mu.Unlock()
	sh := y.ships[id]
	fn(sh)
	return *sh
}

// busy reports whether a ship of this app is still going.
func (y *shipyard) busy(repo, stack string) bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	for _, sh := range y.ships {
		if sh.Repo == repo && sh.Stack == stack && sh.Finished == nil {
			return true
		}
	}
	return false
}

func (s *Server) postShip(w http.ResponseWriter, r *http.Request) {
	var t releaseTarget
	if !readJSON(w, r, 4<<10, &t) {
		return
	}
	a, status, err := s.app(r, t)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	sh, status, err := s.ship(r.Context(), a, t.Number, t.Force, releaseTrigger(r))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, sh)
}

// ship merges a pull request and follows it through to a deploy.
func (s *Server) ship(ctx context.Context, a *releases.App, number int, force bool, trigger string) (*Ship, int, error) {
	if s.ships.busy(a.Repo, a.Stack) {
		return nil, http.StatusConflict, errors.New("a merge and deploy of " + a.Repo + " is already under way")
	}
	if st, ok := s.Config.Stack(a.Stack); ok && st.Pin != nil {
		return nil, http.StatusConflict, errors.New(st.Name + " is rolled back; resume :latest on its page first")
	}
	p, ok := findPR(a, number)
	if !ok {
		return nil, http.StatusNotFound, fmt.Errorf("%s has no open pull request #%d", a.Repo, number)
	}
	title := p.Title
	res, status, err := s.merge(ctx, a, number, force, trigger)
	if err != nil {
		return nil, status, err
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	run, cancel := context.WithTimeout(context.Background(), shipTimeout)
	sh := &Ship{
		ID: hex.EncodeToString(b), Repo: a.Repo, Stack: a.Stack, Number: number, Title: title, SHA: res.SHA,
		State: ShipBuilding, Message: "Merged; waiting for the image build", Started: time.Now().UTC(),
		trigger: trigger, cancel: cancel,
	}
	s.ships.mu.Lock()
	s.ships.ships[sh.ID] = sh
	out := *sh // follow changes sh from here on
	s.ships.mu.Unlock()
	go s.follow(run, cancel, sh.ID, *a)
	return &out, http.StatusAccepted, nil
}

// follow waits for the merged commit's image build, then deploys.
func (s *Server) follow(ctx context.Context, cancel context.CancelFunc, id string, a releases.App) {
	defer cancel()
	sh := s.ships.update(id, func(*Ship) {})
	finish := func(state, msg string) {
		now := time.Now().UTC()
		sh = s.ships.update(id, func(x *Ship) {
			if x.Finished == nil {
				x.State, x.Message, x.Finished = state, msg, &now
			}
		})
		s.Releases.Forget()
		if state == ShipFailed {
			s.record(audit.Event{Stack: a.Stack, Services: a.Services, Action: audit.ReleaseShip, Trigger: sh.trigger,
				Detail: fmt.Sprintf("%s#%d: %s", a.Repo, sh.Number, sh.Title), Commit: shortSHA(sh.SHA), Result: audit.Failed, Error: msg})
		}
		if state != ShipCancelled {
			kind := "success"
			if state == ShipFailed {
				kind = "failure"
			}
			s.notify(s.Config.Releases.Notify, kind, "Hoist: "+a.Repo+"#"+fmt.Sprint(sh.Number), msg)
		}
	}
	started := time.Now()
	for {
		runs, err := s.Releases.GitHub.WorkflowRuns(ctx, a.Repo, s.Config.Releases.Workflow, a.Branch, sh.SHA)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			slog.Debug("ship: image build", "repo", a.Repo, "err", err)
		case len(runs) == 0 && time.Since(started) > shipStart:
			finish(ShipFailed, "Merged, but no image build started for "+shortSHA(sh.SHA))
			return
		case len(runs) > 0:
			r := runs[0]
			s.ships.update(id, func(x *Ship) { x.BuildURL = r.URL })
			if r.Failed() {
				finish(ShipFailed, "Merged, but the image build failed: "+r.URL)
				return
			}
			if r.Done() {
				s.deployShip(ctx, id, a, finish)
				return
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				finish(ShipFailed, "Merged, but the image build took too long")
			} else {
				finish(ShipCancelled, "Cancelled after the merge; nothing was deployed")
			}
			return
		case <-time.After(shipPoll):
		}
	}
}

func (s *Server) deployShip(ctx context.Context, id string, a releases.App, finish func(string, string)) {
	sh := s.ships.update(id, func(x *Ship) { x.State, x.Message = ShipDeploying, "Image built; deploying" })
	s.Releases.Forget()
	job, _, err := s.deployApp(&a, sh.trigger)
	if err != nil {
		finish(ShipFailed, "The image was built, but the deploy didn't start: "+err.Error())
		return
	}
	s.ships.update(id, func(x *Ship) { x.Job = job.ID })
	for {
		select {
		case <-ctx.Done():
			// The deploy carries on; its page says how it went.
			finish(ShipDone, "Deploying; follow it on its page")
			return
		case <-time.After(shipPoll / 3):
		}
		j, err := s.Jobs.Get(job.ID)
		if err != nil || j.State == jobs.Running {
			continue
		}
		if j.State == jobs.Failed {
			finish(ShipFailed, "The deploy failed: "+j.Error)
		} else {
			finish(ShipDone, "Deployed: "+j.Result.Summary())
		}
		return
	}
}

// dismiss drops a finished ship from the board before shipKeep is up.
func (y *shipyard) dismiss(id string) bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	sh, ok := y.ships[id]
	if !ok || sh.Finished == nil {
		return false
	}
	delete(y.ships, id)
	return true
}

// deleteShip cancels a ship that hasn't started its deploy, or dismisses
// one that has finished.
func (s *Server) deleteShip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.ships.dismiss(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.ships.mu.Lock()
	sh, ok := s.ships.ships[id]
	var state string
	if ok {
		state = sh.State
	}
	s.ships.mu.Unlock()
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "no such merge and deploy")
		return
	case state != ShipBuilding:
		writeError(w, http.StatusConflict, "it's past the point of cancelling ("+state+")")
		return
	}
	sh.cancel()
	s.record(audit.Event{Stack: sh.Stack, Action: audit.ReleaseShip, Trigger: releaseTrigger(r),
		Detail: fmt.Sprintf("%s#%d: cancelled the deploy after the merge", sh.Repo, sh.Number), Commit: shortSHA(sh.SHA)})
	w.WriteHeader(http.StatusNoContent)
}

// shipsOf picks an app's ships.
func shipsOf(all []Ship, a releases.App) []Ship {
	return slices.DeleteFunc(slices.Clone(all), func(sh Ship) bool { return sh.Repo != a.Repo || sh.Stack != a.Stack })
}
