// Package updates checks the stacks' images against their registries: has
// the tag moved to a new image, and is there a newer version than the one
// pinned? Checks never pull; results are kept in /config/updates.json.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/registry"
)

// Candidate is a newer version of a pinned tag.
type Candidate struct {
	Tag  string `json:"tag"`
	Bump string `json:"bump"` // major | minor | patch
}

// Service is what a check found for one service.
type Service struct {
	Service string `json:"service"`
	Image   string `json:"image"`
	// NewImage is set when the tag points to a different image than the
	// running container's: a deploy (which pulls) would update it.
	NewImage bool `json:"new_image,omitempty"`
	// Latest is the newest same-shaped version tag, if newer than the
	// current one. Allowed is the newest the service's policy would apply.
	Latest  *Candidate `json:"latest,omitempty"`
	Allowed *Candidate `json:"allowed,omitempty"`
	Policy  string     `json:"policy"`
	// Skipped says why the image wasn't checked (built locally, pinned by
	// digest).
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Pending reports whether there's something to update.
func (s Service) Pending() bool { return s.NewImage || s.Latest != nil }

type State struct {
	CheckedAt *time.Time           `json:"checked_at,omitempty"`
	Stacks    map[string][]Service `json:"stacks"`
}

// Count is how many services have an update, in all stacks or one.
func (st State) Count(stack string) int {
	n := 0
	for name, svcs := range st.Stacks {
		if stack != "" && name != stack {
			continue
		}
		for _, s := range svcs {
			if s.Pending() {
				n++
			}
		}
	}
	return n
}

type Checker struct {
	cfg  *config.Config
	dock *docker.Client
	reg  *registry.Client
	path string

	mu       sync.Mutex
	state    State
	checking bool
}

func New(cfg *config.Config, dock *docker.Client, path string) *Checker {
	c := &Checker{cfg: cfg, dock: dock, reg: registry.New(), path: path, state: State{Stacks: map[string][]Service{}}}
	if data, err := os.ReadFile(path); err == nil {
		var st State
		if json.Unmarshal(data, &st) == nil && st.Stacks != nil {
			c.state = st
		}
	}
	return c
}

// State returns the last results, and whether a check is running.
func (c *Checker) State() (State, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.checking
}

// ErrBusy means a check is already running.
var ErrBusy = errors.New("a check is already running")

// Check asks the registries about every stack's images. It takes a minute
// or so for a big stack; ErrBusy when one is already running.
func (c *Checker) Check(ctx context.Context) (State, error) {
	c.mu.Lock()
	if c.checking {
		c.mu.Unlock()
		return State{}, ErrBusy
	}
	c.checking = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.checking = false
		c.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	st := State{Stacks: map[string][]Service{}}
	tagCache := map[string][]string{}
	var cacheMu sync.Mutex
	for _, stack := range c.cfg.List() {
		svcs, err := compose.Services(ctx, stack)
		if err != nil {
			slog.Warn("update check: compose", "stack", stack.Name, "err", err)
			continue
		}
		containers, _ := c.dock.Project(ctx, stack.Project)
		running := map[string]string{} // service → image ID
		for _, ct := range containers {
			if !ct.OneOff {
				running[ct.Service] = ct.ImageID
			}
		}
		out := make([]Service, len(svcs))
		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for i, svc := range svcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				out[i] = c.checkService(ctx, stack, svc, running[svc.Name], tagCache, &cacheMu)
			}()
		}
		wg.Wait()
		st.Stacks[stack.Name] = out
	}
	now := time.Now().UTC()
	st.CheckedAt = &now
	c.mu.Lock()
	c.state = st
	c.mu.Unlock()
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(c.path, data, 0o644)
	}
	return st, nil
}

func (c *Checker) checkService(ctx context.Context, stack config.Stack, svc compose.Service, imageID string,
	tagCache map[string][]string, cacheMu *sync.Mutex) Service {
	out := Service{Service: svc.Name, Image: svc.Image, Policy: c.cfg.Policy(stack, svc.Name)}
	ref, err := registry.Parse(svc.Image)
	switch {
	case svc.Image == "":
		out.Skipped = "built locally"
		return out
	case err != nil:
		out.Error = err.Error()
		return out
	case ref.Digest != "":
		out.Skipped = "pinned by digest"
		return out
	}

	// Has the tag moved on from the running image?
	if imageID != "" {
		digests, err := c.dock.RepoDigests(ctx, imageID)
		if err == nil && len(digests) == 0 {
			out.Skipped = "built locally"
		}
		if len(digests) > 0 {
			remote, err := c.reg.Digest(ctx, ref)
			if err != nil {
				out.Error = err.Error()
				return out
			}
			out.NewImage = !hasDigest(digests, ref.Name(), remote)
		}
	}

	// Is there a newer version than a pinned one?
	cur, ok := registry.ParseVersion(ref.Tag)
	if !ok {
		return out
	}
	key := ref.Registry + "/" + ref.Repo
	cacheMu.Lock()
	tags, cached := tagCache[key]
	cacheMu.Unlock()
	if !cached {
		tags, err = c.reg.Tags(ctx, ref)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		cacheMu.Lock()
		tagCache[key] = tags
		cacheMu.Unlock()
	}
	newer := registry.Newer(cur, tags)
	if len(newer) == 0 {
		return out
	}
	last := newer[len(newer)-1]
	out.Latest = &Candidate{Tag: last.Tag, Bump: cur.Bump(last)}
	for i := len(newer) - 1; i >= 0; i-- {
		if b := cur.Bump(newer[i]); registry.Allows(out.Policy, b) {
			out.Allowed = &Candidate{Tag: newer[i].Tag, Bump: b}
			break
		}
	}
	return out
}

// hasDigest reports whether an image's RepoDigests include digest for repo.
func hasDigest(repoDigests []string, repo, digest string) bool {
	for _, rd := range repoDigests {
		name, d, ok := strings.Cut(rd, "@")
		if ok && d == digest && (name == repo || strings.TrimPrefix(name, "docker.io/") == repo) {
			return true
		}
	}
	return false
}

// Forget drops a service's result once it has been updated, so the page
// doesn't offer it again until the next check.
func (c *Checker) Forget(stack string, services ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.state.Stacks[stack]
	for i := range list {
		for _, s := range services {
			if list[i].Service == s {
				list[i].NewImage, list[i].Latest, list[i].Allowed = false, nil, nil
			}
		}
	}
}

// Sorted returns a stack's pending updates, by service.
func (st State) Sorted(stack string) []Service {
	var out []Service
	for _, s := range st.Stacks[stack] {
		if s.Pending() {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}
