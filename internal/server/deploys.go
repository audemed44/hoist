package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/deploy"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/gitrepo"
	"github.com/audemed44/hoist/internal/jobs"
)

var errBusy = errors.New("a deploy of this stack is already running")

// HelperAlive reports whether a self deploy's helper container is still
// around. When Docker can't be asked, it assumes so.
func HelperAlive(dock *docker.Client) func(*jobs.Job) bool {
	return func(j *jobs.Job) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ok, err := dock.Exists(ctx, jobs.HelperName(j.ID))
		return ok || err != nil
	}
}

// active returns a stack's running deploy. A self deploy whose helper
// container is gone without recording a result (it crashed, or someone
// removed it) is marked failed, so it doesn't block the next deploy.
func (s *Server) active(stack string) *jobs.Job {
	j := s.Jobs.Active(stack)
	if j == nil || !j.Self || time.Since(j.Started) < 15*time.Second || HelperAlive(s.Docker)(j) {
		return j
	}
	// The helper may have finished between the two reads.
	if j, err := s.Jobs.Get(j.ID); err != nil || j.State != jobs.Running {
		return nil
	}
	_ = s.Jobs.Finish(j, nil, errors.New("the helper container stopped before finishing the deploy"))
	return nil
}

// startDeploy records a job and runs it in the background: in this process,
// or, for Hoist's own stack, in a helper container that outlives it.
func (s *Server) startDeploy(st config.Stack, services []string, trigger, commit string) (*jobs.Job, int, error) {
	s.mu.Lock()
	if s.starting[st.Name] || s.active(st.Name) != nil {
		s.mu.Unlock()
		return nil, http.StatusConflict, errBusy
	}
	s.starting[st.Name] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.starting, st.Name)
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if len(services) > 0 {
		known, err := s.services(ctx, st)
		if err != nil {
			return nil, http.StatusUnprocessableEntity, err
		}
		for _, name := range services {
			if !slices.ContainsFunc(known, func(k compose.Service) bool { return k.Name == name }) {
				return nil, http.StatusUnprocessableEntity, fmt.Errorf("no service %q in %s", name, st.File)
			}
		}
	}
	dirty := false
	if repo, _ := gitrepo.Open(ctx, st.Path); repo != nil {
		if status, err := repo.Status(ctx, st.ComposePath(), 0); err == nil {
			if commit == "" {
				commit = status.Head
			}
			dirty = status.Modified
		}
	}
	self := s.isSelf(st)
	job, err := s.Jobs.Create(jobs.Job{Stack: st.Name, Services: services, Trigger: trigger, Commit: commit, Dirty: dirty, Self: self})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if !self {
		go func() {
			if err := deploy.Run(context.Background(), s.Docker, s.Jobs, st, job); err != nil {
				slog.Warn("deploy failed", "stack", st.Name, "job", job.ID, "err", err)
			}
		}()
		return job, http.StatusAccepted, nil
	}
	exe, err := os.Executable()
	if err == nil {
		_, err = s.Docker.RunHelper(ctx, s.findSelf(), jobs.HelperName(job.ID), []string{exe, "job", job.ID})
	}
	if err != nil {
		err = fmt.Errorf("could not start the helper container: %w", err)
		_ = s.Jobs.Finish(job, nil, err)
		return nil, http.StatusInternalServerError, err
	}
	return job, http.StatusAccepted, nil
}

func (s *Server) postDeploy(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	var body struct {
		Services []string `json:"services"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, 16<<10, &body) {
		return
	}
	job, status, err := s.startDeploy(st, body.Services, "ui", "")
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, status, job)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	writeJSON(w, http.StatusOK, s.Jobs.List(r.URL.Query().Get("stack"), limit))
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Jobs.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// jobLog streams a job's output as plain text until the job ends.
func (s *Server) jobLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.Jobs.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	f, err := os.Open(s.Jobs.LogPath(id))
	if err != nil {
		writeError(w, http.StatusNotFound, "no log for this job")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Reverse proxies shouldn't hold the stream back.
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	for {
		finished := job.State != jobs.Running
		if _, err := io.Copy(w, f); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if finished {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		if job, err = s.Jobs.Get(id); err != nil {
			return
		}
	}
}
