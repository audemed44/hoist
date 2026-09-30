package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/envfile"
	"github.com/audemed44/hoist/internal/gitrepo"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/updates"
)

type updatesResponse struct {
	updates.State
	Checking bool   `json:"checking"`
	Every    string `json:"every"` // "6h0m0s", or "" when only on demand
	Auto     string `json:"auto"`
	Count    int    `json:"count"`
}

func (s *Server) getUpdates(w http.ResponseWriter, _ *http.Request) {
	st, checking := s.Updates.State()
	resp := updatesResponse{State: st, Checking: checking, Auto: s.Config.Updates.Auto, Count: st.Count("")}
	if d := s.Config.Updates.Interval(); d > 0 {
		resp.Every = d.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// postCheck starts a check in the background; the page polls getUpdates.
func (s *Server) postCheck(w http.ResponseWriter, _ *http.Request) {
	if _, checking := s.Updates.State(); checking {
		writeError(w, http.StatusConflict, updates.ErrBusy.Error())
		return
	}
	go s.checkUpdates(context.Background(), false)
	w.WriteHeader(http.StatusAccepted)
}

// RunUpdates checks on the configured schedule until ctx ends, applying
// whatever the update policies allow after each scheduled check.
func (s *Server) RunUpdates(ctx context.Context) {
	every := s.Config.Updates.Interval()
	if every == 0 {
		return
	}
	// Leave the stacks be for a bit after Hoist starts (it may have just
	// been updated itself).
	wait := 2 * time.Minute
	if st, _ := s.Updates.State(); st.CheckedAt != nil {
		if next := time.Until(st.CheckedAt.Add(every)); next > wait {
			wait = next
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s.checkUpdates(ctx, !s.ReadOnly)
		wait = every
	}
}

func (s *Server) checkUpdates(ctx context.Context, apply bool) {
	st, err := s.Updates.Check(ctx)
	if err != nil {
		if !errors.Is(err, updates.ErrBusy) {
			slog.Warn("update check failed", "err", err)
		}
		return
	}
	slog.Info("update check done", "updates", st.Count(""))
	if apply {
		s.autoUpdate(st)
	}
}

// autoUpdate applies what each service's policy allows: new images behind
// the same tag (digest and up), and version bumps within patch or minor.
func (s *Server) autoUpdate(st updates.State) {
	for _, stack := range s.Config.List() {
		var bumps []bump
		var pull []string
		for _, u := range st.Stacks[stack.Name] {
			if u.Policy == "" || u.Policy == "off" || u.Error != "" {
				continue
			}
			if u.Allowed != nil {
				bumps = append(bumps, bump{Service: u.Service, Image: u.Image, Tag: u.Allowed.Tag})
			} else if u.NewImage {
				pull = append(pull, u.Service)
			}
		}
		if len(bumps) == 0 && len(pull) == 0 {
			continue
		}
		job, err := s.applyUpdates(stack, bumps, pull, "auto")
		if err != nil {
			slog.Warn("automatic update not applied", "stack", stack.Name, "err", err)
			s.notify("failure", "Hoist: couldn't update "+stack.Name, err.Error())
			continue
		}
		go s.notifyWhenDone(job)
	}
}

type bump struct {
	Service string
	Image   string
	Tag     string
}

// applyUpdates commits version bumps to the compose file (pushed like any
// edit), then deploys the bumped services plus the ones in pull.
func (s *Server) applyUpdates(st config.Stack, bumps []bump, pull []string, trigger string) (*jobs.Job, error) {
	ev := audit.Event{Stack: st.Name, Action: audit.UpdateApply, Trigger: trigger, Detail: updateDetail(bumps, pull)}
	for _, b := range bumps {
		ev.Services = append(ev.Services, b.Service)
	}
	ev.Services = append(ev.Services, pull...)
	job, commit, err := s.applyUpdatesRun(st, bumps, pull, trigger)
	ev.Commit = commit
	if err != nil {
		ev.Result, ev.Error = audit.Failed, err.Error()
	} else {
		ev.Job = job.ID
	}
	s.record(ev)
	return job, err
}

// updateDetail is e.g. "shelfloom → 0.5; new image for foyer".
func updateDetail(bumps []bump, pull []string) string {
	var parts []string
	for _, b := range bumps {
		parts = append(parts, b.Service+" → "+b.Tag)
	}
	if len(pull) > 0 {
		parts = append(parts, "new image for "+strings.Join(pull, ", "))
	}
	return strings.Join(parts, "; ")
}

func (s *Server) applyUpdatesRun(st config.Stack, bumps []bump, pull []string, trigger string) (*jobs.Job, string, error) {
	services := append([]string{}, pull...)
	commit := ""
	if len(bumps) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		repo, err := gitrepo.Open(ctx, st.Path)
		if err != nil {
			return nil, commit, err
		}
		if repo != nil {
			status, err := repo.Status(ctx, st.ComposePath(), 0)
			if err != nil {
				return nil, commit, err
			}
			if status.Modified {
				return nil, commit, errors.New("the compose file has uncommitted changes; commit or discard them first")
			}
		}
		current, err := os.ReadFile(st.ComposePath())
		if err != nil {
			return nil, commit, err
		}
		content := compose.ToLF(current)
		for _, b := range bumps {
			if content, _, err = compose.BumpImage(content, b.Image, b.Tag); err != nil {
				return nil, commit, fmt.Errorf("%s: %w", b.Service, err)
			}
			services = append(services, b.Service)
		}
		if err := compose.Validate(ctx, st, content); err != nil {
			return nil, commit, err
		}
		if err := envfile.WriteAtomic(st.ComposePath(), content, 0o644); err != nil {
			return nil, commit, err
		}
		var res saveResult
		if err := s.commit(ctx, st, compose.Summary(st.Name, current, content), &res); err != nil {
			return nil, commit, fmt.Errorf("the compose file was changed, but the commit failed: %w", err)
		}
		commit = res.Commit
		if res.PushError != "" {
			slog.Warn("update committed but not pushed", "stack", st.Name, "err", res.PushError)
		}
	}
	job, _, err := s.startDeploy(st, services, trigger, commit)
	if err != nil {
		return nil, commit, err
	}
	s.Updates.Forget(st.Name, services...)
	return job, commit, nil
}

// applyUpdate bumps one service to a version the check found, and deploys it.
func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	service := r.PathValue("service")
	var body struct {
		Tag string `json:"tag"`
	}
	if !readJSON(w, r, 4<<10, &body) {
		return
	}
	state, _ := s.Updates.State()
	var found *updates.Service
	for _, u := range state.Stacks[st.Name] {
		if u.Service == service {
			found = &u
		}
	}
	switch {
	case found == nil:
		writeError(w, http.StatusNotFound, "no update check result for "+service)
		return
	case body.Tag == "" && !found.NewImage:
		writeError(w, http.StatusBadRequest, "no new image for "+service)
		return
	case body.Tag != "" && (found.Latest == nil || !newerOK(*found, body.Tag)):
		writeError(w, http.StatusBadRequest, body.Tag+" isn't an update the check found")
		return
	}
	var bumps []bump
	var pull []string
	if body.Tag != "" {
		bumps = []bump{{Service: service, Image: found.Image, Tag: body.Tag}}
	} else {
		pull = []string{service}
	}
	job, err := s.applyUpdates(st, bumps, pull, triggerOf(r))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// newerOK accepts the tags the check offered: the latest, or the newest the
// policy allows.
func newerOK(u updates.Service, tag string) bool {
	return (u.Latest != nil && u.Latest.Tag == tag) || (u.Allowed != nil && u.Allowed.Tag == tag)
}

// notifyWhenDone reports an automatic update's outcome.
func (s *Server) notifyWhenDone(job *jobs.Job) {
	for range 360 { // up to 30 minutes
		time.Sleep(5 * time.Second)
		j, err := s.Jobs.Get(job.ID)
		if err != nil || j.State == jobs.Running {
			continue
		}
		if j.State == jobs.Failed {
			s.notify("failure", "Hoist: update of "+j.Stack+" failed", j.Error)
		} else {
			s.notify("success", "Hoist: updated "+j.Stack, j.Result.Summary())
		}
		return
	}
}

// notify posts to an Apprise API endpoint, when one is configured.
func (s *Server) notify(kind, title, body string) {
	url := s.Config.Updates.Notify
	if url == "" {
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body, "type": kind})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("notify failed", "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("notify failed", "status", resp.StatusCode, "title", strings.TrimSpace(title))
	}
}
