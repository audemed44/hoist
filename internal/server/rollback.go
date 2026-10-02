package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/deploy"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/gitrepo"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/registry"
)

// Every finished deploy records what the stack ran (audit.DeployRecord).
// Once its containers have run for rollback.healthy_for without restarting
// or failing a healthcheck, it counts as good, and its images are tagged
// hoist-keep/<stack> so a prune leaves them for a rollback. A rollback pins
// the stack to an earlier deploy's images by digest (config.Pin) until
// it's resumed.

// keepRepo is the local repository the images of good deploys are tagged in.
func keepRepo(stack string) string { return "hoist-keep/" + stack }

// pinInfo is a pinned stack, as the UI shows it.
type pinInfo struct {
	Deploy string            `json:"deploy"`
	At     time.Time         `json:"at"`
	Commit string            `json:"commit,omitempty"`
	Images map[string]string `json:"images"`
	// OldCompose is set when the pin uses the older deploy's compose file
	// instead of the stack's.
	OldCompose bool `json:"old_compose,omitempty"`
}

func newPinInfo(p *config.Pin) *pinInfo {
	if p == nil {
		return nil
	}
	return &pinInfo{Deploy: p.Deploy, At: p.At, Commit: p.Commit, Images: p.Images, OldCompose: p.Base != ""}
}

// judgement is when a stack's latest deploy was last looked at, and
// whether it's settled (good, or failed): then there's nothing to look at
// until the next deploy.
type judgement struct {
	at      time.Time
	settled bool
}

// judge looks at a stack's latest deploy, at most once a minute until it's
// settled: records the running stack as a baseline when Hoist has no
// deploy of it on record, and marks the latest deploy good once it has
// proven itself.
func (s *Server) judge(ctx context.Context, st config.Stack, force bool) {
	s.mu.Lock()
	if j := s.judged[st.Name]; j.settled || (!force && time.Since(j.at) < time.Minute) {
		s.mu.Unlock()
		return
	}
	s.judged[st.Name] = judgement{at: time.Now()}
	s.mu.Unlock()
	settle := func() {
		s.mu.Lock()
		s.judged[st.Name] = judgement{at: time.Now(), settled: true}
		s.mu.Unlock()
	}
	if s.active(st.Name) != nil {
		return
	}
	recs, err := s.Audit.Deploys(ctx, st.Name, 1)
	if err != nil {
		slog.Debug("deploy records", "stack", st.Name, "err", err)
		return
	}
	if len(recs) == 0 {
		s.baseline(ctx, st)
		return
	}
	d := recs[0]
	if d.GoodAt != nil || d.Result != audit.OK {
		settle()
		return
	}
	if time.Since(d.Finished) < s.Config.Rollback.Healthy() || !s.healthy(ctx, d) {
		return
	}
	if err := s.Audit.MarkGood(ctx, d.Job, time.Now()); err != nil {
		slog.Warn("could not mark a deploy good", "stack", st.Name, "job", d.Job, "err", err)
		return
	}
	settle()
	s.protect(ctx, st)
}

// baseline records what a stack runs when Hoist has no deploy of it on
// record, so the first deploy through Hoist can be rolled back.
func (s *Server) baseline(ctx context.Context, st config.Stack) {
	images := deploy.Snapshot(ctx, s.Docker, st.Project)
	if len(images) == 0 {
		return
	}
	now := time.Now().UTC()
	j := &jobs.Job{ID: jobs.NewID(), Stack: st.Name, Trigger: "baseline", State: jobs.Done, Started: now, Finished: &now, Images: images}
	if repo, _ := gitrepo.Open(ctx, st.Path); repo != nil {
		if status, err := repo.Status(ctx, st.ComposePath(), 0); err == nil {
			j.Commit, j.Dirty = status.Head, status.Modified
		}
	}
	if err := s.Audit.RecordDeploy(ctx, j); err != nil {
		slog.Warn("could not record the stack's state", "stack", st.Name, "err", err)
	}
}

// healthy reports whether the containers a deploy left running are all
// still those containers, running, not unhealthy, and haven't restarted
// for healthy_for. Ones Gatehouse has put to sleep don't count against it.
func (s *Server) healthy(ctx context.Context, d audit.DeployRecord) bool {
	asleep := s.Sleep.Asleep(ctx)
	for _, img := range d.Images {
		if img.State != "running" {
			continue
		}
		h, err := s.Docker.Health(ctx, img.ContainerID)
		if err != nil {
			return false // replaced since, or docker can't say
		}
		if !h.Running {
			if _, ok := asleep[img.Container]; ok {
				continue
			}
			return false
		}
		if h.Status == "unhealthy" || h.Status == "starting" || time.Since(h.StartedAt) < s.Config.Rollback.Healthy() {
			return false
		}
	}
	return true
}

// protect tags the images of the last rollback.keep good deploys of a
// stack, and untags older ones.
func (s *Server) protect(ctx context.Context, st config.Stack) {
	recs, err := s.Audit.Deploys(ctx, st.Name, 100)
	if err != nil {
		return
	}
	want := map[string]string{} // tag → image ID
	kept := 0
	for _, d := range recs {
		if d.GoodAt == nil || kept >= s.Config.Rollback.Keep {
			continue
		}
		kept++
		for _, img := range d.Images {
			if img.Digest == "" || img.ImageID == "" {
				continue
			}
			want[keepRepo(st.Name)+":"+d.Job+"-"+tagSafe(img.Service)] = img.ImageID
		}
	}
	have, err := s.Docker.Tagged(ctx, keepRepo(st.Name))
	if err != nil {
		slog.Debug("list kept images", "stack", st.Name, "err", err)
		return
	}
	for tag, id := range want {
		if slices.Contains(have, tag) {
			continue
		}
		repo, t, _ := strings.Cut(tag, ":")
		if err := s.Docker.Tag(ctx, id, repo, t); err != nil && !errors.Is(err, docker.ErrNotFound) {
			slog.Warn("could not keep an image for rollback", "tag", tag, "err", err)
		}
	}
	for _, tag := range have {
		if _, ok := want[tag]; !ok {
			if err := s.Docker.Untag(ctx, tag); err != nil {
				slog.Debug("untag", "tag", tag, "err", err)
			}
		}
	}
}

// tagSafe makes a service name usable in an image tag.
func tagSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, s)
}

// deployEntry is a deploy record as the Deploys tab lists it.
type deployEntry struct {
	audit.DeployRecord
	// Current is the deploy the stack runs now.
	Current bool `json:"current,omitempty"`
}

func (s *Server) listDeploys(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	s.judge(r.Context(), st, false)
	recs, err := s.Audit.Deploys(r.Context(), st.Name, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]deployEntry, len(recs))
	for i, d := range recs {
		out[i] = deployEntry{DeployRecord: d, Current: i == 0}
	}
	writeJSON(w, http.StatusOK, out)
}

// rollbackService is one service's change in a rollback.
type rollbackService struct {
	Service string      `json:"service"`
	From    *jobs.Image `json:"from,omitempty"`
	To      jobs.Image  `json:"to"`
	// Pinned is the reference the rollback runs it with; "" when it
	// can't be pinned (built locally) and stays as it is.
	Pinned  string `json:"pinned,omitempty"`
	Changes bool   `json:"changes"`
	// Where is where the image comes from: "host" or "registry".
	Where   string `json:"where,omitempty"`
	Problem string `json:"problem,omitempty"`
}

type rollbackPlan struct {
	Target   audit.DeployRecord `json:"target"`
	Services []rollbackService  `json:"services"`
	// Compose is the older compose file, when it differs from the
	// stack's current one; Current is that one, for a diff.
	Compose *composeFile `json:"compose,omitempty"`
	Current *composeFile `json:"current,omitempty"`
	// Notes are things to know, Problem is why it can't be done.
	Notes   []string `json:"notes"`
	Problem string   `json:"problem,omitempty"`
}

// defaultTarget is the last good deploy before the current one that ran
// something different.
func defaultTarget(recs []audit.DeployRecord) (audit.DeployRecord, bool) {
	if len(recs) == 0 {
		return audit.DeployRecord{}, false
	}
	cur := recs[0]
	for _, d := range recs[1:] {
		if d.GoodAt != nil && d.Result == audit.OK && !sameImages(cur.Images, d.Images) {
			return d, true
		}
	}
	return audit.DeployRecord{}, false
}

func sameImages(a, b []jobs.Image) bool {
	key := func(imgs []jobs.Image) string {
		var parts []string
		for _, i := range imgs {
			parts = append(parts, i.Service+"="+i.ImageID)
		}
		sort.Strings(parts)
		return strings.Join(parts, ",")
	}
	return key(a) == key(b)
}

// plan works out a rollback to the deploy job ("" for the default target).
func (s *Server) plan(ctx context.Context, st config.Stack, job string) (*rollbackPlan, int, error) {
	var target audit.DeployRecord
	if job == "" {
		recs, err := s.Audit.Deploys(ctx, st.Name, 100)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		var ok bool
		if target, ok = defaultTarget(recs); !ok {
			return nil, http.StatusNotFound, errors.New("there's no earlier good deploy to roll back to yet")
		}
	} else {
		var err error
		if target, err = s.Audit.DeployOf(ctx, job); err != nil {
			return nil, http.StatusNotFound, err
		}
		if target.Stack != st.Name {
			return nil, http.StatusNotFound, audit.ErrNoDeploy
		}
	}
	p := &rollbackPlan{Target: target, Services: []rollbackService{}, Notes: []string{}}
	if target.Result != audit.OK {
		p.Problem = "that deploy failed, so there's no known state to go back to"
	}

	// The compose file it used.
	current, _ := os.ReadFile(st.ComposePath())
	cur := newComposeFile(current)
	if target.Commit != "" {
		if repo, _ := gitrepo.Open(ctx, st.Path); repo != nil {
			rel, _ := repo.Rel(st.ComposePath())
			old, err := repo.Show(ctx, target.Commit, rel)
			switch {
			case err != nil:
				p.Notes = append(p.Notes, "The compose file of "+target.Commit+" can't be read, so the current one is used.")
			case !bytes.Equal(compose.ToLF(old), compose.ToLF(current)):
				f := newComposeFile(old)
				p.Compose, p.Current = &f, &cur
			}
		}
	}
	if target.Dirty {
		p.Notes = append(p.Notes, "That deploy ran a compose file with uncommitted changes; the version of its commit is used.")
	}

	// Each service: what runs now, what it ran then, and whether that
	// image can still be had.
	now := map[string]jobs.Image{}
	for _, img := range deploy.Snapshot(ctx, s.Docker, st.Project) {
		now[img.Service] = img
	}
	seen := map[string]bool{}
	for _, img := range target.Images {
		if seen[img.Service] { // replicas
			continue
		}
		seen[img.Service] = true
		rs := rollbackService{Service: img.Service, To: img}
		if from, ok := now[img.Service]; ok {
			rs.From = &from
			rs.Changes = from.ImageID != img.ImageID
		} else {
			rs.Changes = true
		}
		if img.Digest == "" {
			rs.Problem = "built locally, so it can't be pinned; it stays as it is"
		} else {
			rs.Pinned = registry.Pinned(img.Ref, img.Digest)
			rs.Where, rs.Problem = s.findImage(ctx, rs.Pinned)
		}
		p.Services = append(p.Services, rs)
	}
	sort.Slice(p.Services, func(i, j int) bool { return p.Services[i].Service < p.Services[j].Service })
	for _, rs := range p.Services {
		if rs.Pinned != "" && rs.Problem != "" && p.Problem == "" {
			p.Problem = rs.Service + "'s image is gone: " + rs.Problem
		}
	}
	return p, http.StatusOK, nil
}

// findImage says whether a pinned image is on this host or still in its
// registry.
func (s *Server) findImage(ctx context.Context, pinned string) (string, string) {
	if _, err := s.Docker.Image(ctx, pinned); err == nil {
		return "host", ""
	}
	ref, err := registry.Parse(pinned)
	if err != nil {
		return "", err.Error()
	}
	if err := s.registry.HasManifest(ctx, ref); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return "", "not on this host, and the registry no longer has it"
		}
		return "", "not on this host, and the registry can't be asked: " + err.Error()
	}
	return "registry", ""
}

func (s *Server) getRollback(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	s.judge(r.Context(), st, false)
	p, status, err := s.plan(r.Context(), st, r.URL.Query().Get("to"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) postRollback(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, 4<<10, &body) {
		return
	}
	job, status, err := s.rollback(r.Context(), st, body.To, triggerOf(r))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// rollback pins st to a deploy's images (and compose file) and deploys it.
func (s *Server) rollback(ctx context.Context, st config.Stack, to, trigger string) (*jobs.Job, int, error) {
	if s.active(st.Name) != nil {
		return nil, http.StatusConflict, errBusy
	}
	p, status, err := s.plan(ctx, st, to)
	if err != nil {
		return nil, status, err
	}
	ev := audit.Event{Stack: st.Name, Action: audit.Rollback, Trigger: trigger,
		Detail: "to the deploy of " + p.Target.Time.Local().Format("2 Jan 15:04"), Commit: p.Target.Commit}
	fail := func(status int, err error) (*jobs.Job, int, error) {
		ev.Result, ev.Error = audit.Failed, err.Error()
		s.record(ev)
		return nil, status, err
	}
	if p.Problem != "" {
		return fail(http.StatusConflict, errors.New(p.Problem))
	}
	pin, err := s.writePin(ctx, st, p)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, err)
	}
	old := st.Pin
	if err := s.Config.SetPin(st.Name, pin); err != nil {
		return fail(http.StatusInternalServerError, err)
	}
	st.Pin = pin
	job, status, err := s.launch(st, jobs.Job{Trigger: trigger, Commit: pin.Commit, Rollback: p.Target.Job})
	if err != nil {
		s.restorePin(st.Name, old)
		return fail(status, err)
	}
	s.Config.PrunePins(st.Name)
	ev.Job = job.ID
	ev.Result = audit.Running
	s.record(ev)
	return job, http.StatusAccepted, nil
}

// restorePin puts back the pin a stack had before a rollback or resume
// that didn't start.
func (s *Server) restorePin(stack string, old *config.Pin) {
	if old != nil {
		if _, err := os.Stat(old.Override); err != nil {
			old = nil
		}
	}
	if err := s.Config.SetPin(stack, old); err != nil {
		slog.Warn("could not restore the stack's pin", "stack", stack, "err", err)
	}
}

// writePin writes the override (and the older compose file, if the plan
// uses it) and checks that compose accepts them.
func (s *Server) writePin(ctx context.Context, st config.Stack, p *rollbackPlan) (*config.Pin, error) {
	dir := filepath.Join(s.Config.PinDir(st.Name), p.Target.Job)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	pin := &config.Pin{Deploy: p.Target.Job, At: time.Now().UTC(), Override: filepath.Join(dir, "override.yml"), Images: map[string]string{}}
	head := ""
	if repo, _ := gitrepo.Open(ctx, st.Path); repo != nil {
		if status, err := repo.Status(ctx, st.ComposePath(), 0); err == nil {
			head = status.Head
		}
	}
	pin.Commit = head
	content, err := os.ReadFile(st.ComposePath())
	if err != nil {
		return nil, err
	}
	if p.Compose != nil {
		pin.Base = filepath.Join(dir, st.File)
		pin.Commit = p.Target.Commit
		content = []byte(p.Compose.Content)
		if err := os.WriteFile(pin.Base, content, 0o644); err != nil {
			return nil, err
		}
	}
	// Only services the base file has: an override naming another would
	// add it.
	svcs, err := compose.Resolve(ctx, st, content)
	if err != nil {
		return nil, fmt.Errorf("the compose file doesn't resolve: %w", err)
	}
	override := map[string]map[string]map[string]string{"services": {}}
	for _, rs := range p.Services {
		if rs.Pinned == "" || !slices.ContainsFunc(svcs, func(c compose.Service) bool { return c.Name == rs.Service }) {
			continue
		}
		override["services"][rs.Service] = map[string]string{"image": rs.Pinned}
		pin.Images[rs.Service] = rs.Pinned
	}
	data, err := yaml.Marshal(override)
	if err != nil {
		return nil, err
	}
	data = append([]byte("# Written by Hoist: "+st.Name+" is rolled back to the deploy "+p.Target.Job+".\n"), data...)
	if err := os.WriteFile(pin.Override, data, 0o644); err != nil {
		return nil, err
	}
	check := st
	check.Pin = pin
	if _, err := compose.Services(ctx, check); err != nil {
		return nil, fmt.Errorf("compose doesn't accept the rollback: %w", err)
	}
	return pin, nil
}

// postResume drops a stack's pin and deploys it as its compose file says.
func (s *Server) postResume(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	if st.Pin == nil {
		writeError(w, http.StatusConflict, "this stack isn't rolled back")
		return
	}
	if s.active(st.Name) != nil {
		writeError(w, http.StatusConflict, errBusy.Error())
		return
	}
	ev := audit.Event{Stack: st.Name, Action: audit.RollbackResume, Trigger: triggerOf(r), Detail: "back to the compose file's images"}
	old := st.Pin
	if err := s.Config.SetPin(st.Name, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st.Pin = nil
	job, status, err := s.launch(st, jobs.Job{Trigger: triggerOf(r)})
	if err != nil {
		s.restorePin(st.Name, old)
		ev.Result, ev.Error = audit.Failed, err.Error()
		s.record(ev)
		writeError(w, status, err.Error())
		return
	}
	s.Config.PrunePins(st.Name)
	ev.Job, ev.Result = job.ID, audit.Running
	s.record(ev)
	writeJSON(w, http.StatusAccepted, job)
}
