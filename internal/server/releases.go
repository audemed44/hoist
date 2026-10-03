package server

import (
	"net/http"
	"time"

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
