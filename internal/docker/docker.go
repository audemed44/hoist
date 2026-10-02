// Package docker is a small client for the Docker Engine API over its unix
// socket. Hoist reads compose projects' containers through it and starts the
// helper container that redeploys Hoist itself; everything else goes through
// docker compose.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

var ErrNotFound = errors.New("not found")

type Client struct {
	http *http.Client
}

func New(socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 15 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var e struct{ Message string }
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			return fmt.Errorf("docker: %s", e.Message)
		}
		return fmt.Errorf("docker: HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Container is one container of a compose project.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	Image   string `json:"image"`
	ImageID string `json:"-"`
	State   string `json:"state"` // running | exited | restarting | paused | created | dead
	Status  string `json:"status"`
	Created int64  `json:"created"`
	// ConfigHash is compose's hash of the service config the container was
	// created from; a different hash means `up` would recreate it.
	ConfigHash string `json:"-"`
	// OneOff marks `compose run` containers, which `up` leaves alone.
	OneOff bool `json:"-"`
	// Project is the compose project, when it belongs to one.
	Project string `json:"-"`
	// Ports are the host ports a running container publishes.
	Ports []Port `json:"-"`
	// Source and Revision are the image's OCI labels (which containers
	// inherit): the repository it was built from, and the commit.
	Source   string `json:"-"`
	Revision string `json:"-"`
}

type Port struct {
	IP       string
	Port     int
	Protocol string
}

const (
	labelProject = "com.docker.compose.project"
	labelDir     = "com.docker.compose.project.working_dir"
	labelFiles   = "com.docker.compose.project.config_files"
	labelService = "com.docker.compose.service"
	labelHash    = "com.docker.compose.config-hash"
	labelOneOff  = "com.docker.compose.oneoff"
)

// Project lists a compose project's containers, stopped ones included.
func (c *Client) Project(ctx context.Context, project string) ([]Container, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {labelProject + "=" + project}})
	return c.containers(ctx, url.Values{"all": {"1"}, "filters": {string(filters)}})
}

// Containers lists every container on the host, stopped ones included.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	return c.containers(ctx, url.Values{"all": {"1"}})
}

func (c *Client) containers(ctx context.Context, q url.Values) ([]Container, error) {
	var items []struct {
		ID      string
		Names   []string
		Image   string
		ImageID string
		State   string
		Status  string
		Created int64
		Labels  map[string]string
		Ports   []struct {
			IP         string
			PublicPort int
			Type       string
		}
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &items); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(items))
	for _, it := range items {
		name := it.ID[:min(12, len(it.ID))]
		if len(it.Names) > 0 {
			name = strings.TrimPrefix(it.Names[0], "/")
		}
		ctr := Container{
			ID: it.ID, Name: name, Service: it.Labels[labelService], Image: it.Image, ImageID: it.ImageID,
			State: it.State, Status: it.Status, Created: it.Created,
			ConfigHash: it.Labels[labelHash], OneOff: it.Labels[labelOneOff] == "True",
			Project: it.Labels[labelProject],
			Source:  it.Labels[LabelSource], Revision: it.Labels[LabelRevision],
		}
		for _, p := range it.Ports {
			if p.PublicPort == 0 {
				continue
			}
			port := Port{IP: p.IP, Port: p.PublicPort, Protocol: p.Type}
			if !slices.Contains(ctr.Ports, port) {
				ctr.Ports = append(ctr.Ports, port)
			}
		}
		out = append(out, ctr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ProjectInfo is a compose project as its containers describe it.
type ProjectInfo struct {
	Name string `json:"name"`
	// Dir and Files are where it was started from, as compose recorded them.
	Dir      string   `json:"dir"`
	Files    []string `json:"files"`
	Services []string `json:"services"`
	Running  int      `json:"running"`
	Total    int      `json:"total"`
}

// Projects lists every compose project with containers, running or not.
func (c *Client) Projects(ctx context.Context) ([]ProjectInfo, error) {
	var items []struct {
		State  string
		Labels map[string]string
	}
	filters, _ := json.Marshal(map[string][]string{"label": {labelProject}})
	q := url.Values{"all": {"1"}, "filters": {string(filters)}}
	if err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &items); err != nil {
		return nil, err
	}
	byName := map[string]*ProjectInfo{}
	for _, it := range items {
		name := it.Labels[labelProject]
		if name == "" || it.Labels[labelOneOff] == "True" {
			continue
		}
		p := byName[name]
		if p == nil {
			p = &ProjectInfo{Name: name, Dir: it.Labels[labelDir], Files: []string{}, Services: []string{}}
			if f := it.Labels[labelFiles]; f != "" {
				p.Files = strings.Split(f, ",")
			}
			byName[name] = p
		}
		if svc := it.Labels[labelService]; svc != "" && !slices.Contains(p.Services, svc) {
			p.Services = append(p.Services, svc)
		}
		p.Total++
		if it.State == "running" {
			p.Running++
		}
	}
	out := make([]ProjectInfo, 0, len(byName))
	for _, p := range byName {
		sort.Strings(p.Services)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Self describes the container Hoist runs in, enough to start a copy of it.
type Self struct {
	ID      string
	Image   string // image ID, so the helper runs exactly this build
	Project string // compose project, if any
	User    string
	Env     []string
	Binds   []string
	Mounts  []Mount
	Groups  []string
}

type Mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

// Exists reports whether a container exists (running or not).
func (c *Client) Exists(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// FindSelf inspects the container this process runs in. Docker sets the
// hostname to the short container ID unless it's overridden, so HOIST_CONTAINER
// can name it instead.
func (c *Client) FindSelf(ctx context.Context) (*Self, error) {
	id := os.Getenv("HOIST_CONTAINER")
	if id == "" {
		id, _ = os.Hostname()
	}
	if id == "" {
		return nil, ErrNotFound
	}
	var info struct {
		ID     string
		Image  string
		Config struct {
			User   string
			Env    []string
			Labels map[string]string
		}
		HostConfig struct {
			Binds    []string
			GroupAdd []string
		}
		Mounts []struct {
			Type        string
			Name        string
			Source      string
			Destination string
			RW          bool
		}
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &info); err != nil {
		return nil, err
	}
	self := &Self{
		ID: info.ID, Image: info.Image, Project: info.Config.Labels[labelProject],
		User: info.Config.User, Env: info.Config.Env, Binds: info.HostConfig.Binds, Groups: info.HostConfig.GroupAdd,
	}
	bound := map[string]bool{}
	for _, b := range info.HostConfig.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			bound[parts[1]] = true
		}
	}
	// Named volumes and --mount binds don't show up in Binds.
	for _, m := range info.Mounts {
		if bound[m.Destination] {
			continue
		}
		switch m.Type {
		case "volume":
			self.Mounts = append(self.Mounts, Mount{Type: "volume", Source: m.Name, Target: m.Destination, ReadOnly: !m.RW})
		case "bind":
			self.Mounts = append(self.Mounts, Mount{Type: "bind", Source: m.Source, Target: m.Destination, ReadOnly: !m.RW})
		}
	}
	return self, nil
}

// RunHelper starts a short-lived copy of self running entrypoint. It isn't
// part of any compose project, so deploying Hoist's stack doesn't touch it,
// and it removes itself when done.
func (c *Client) RunHelper(ctx context.Context, self *Self, name string, entrypoint []string) (string, error) {
	body := map[string]any{
		"Image":      self.Image,
		"Entrypoint": entrypoint,
		"Cmd":        []string{},
		// The image's healthcheck is for the server, which the helper isn't.
		"Healthcheck": map[string]any{"Test": []string{"NONE"}},
		"User":        self.User,
		"Env":         self.Env,
		"Labels":      map[string]string{"hoist.helper": "true"},
		"HostConfig": map[string]any{
			"Binds":      self.Binds,
			"Mounts":     self.Mounts,
			"GroupAdd":   self.Groups,
			"AutoRemove": true,
		},
	}
	var created struct{ ID string }
	q := url.Values{"name": {name}}
	if err := c.do(ctx, http.MethodPost, "/containers/create", q, body, &created); err != nil {
		return "", err
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil, nil); err != nil {
		_ = c.do(ctx, http.MethodDelete, "/containers/"+created.ID, url.Values{"force": {"1"}}, nil, nil)
		return "", err
	}
	return created.ID, nil
}

// ImageHealthcheck reports whether a local image defines a healthcheck;
// ErrNotFound means it isn't pulled.
func (c *Client) ImageHealthcheck(ctx context.Context, image string) (bool, error) {
	var info struct {
		Config struct {
			Healthcheck *struct{ Test []string }
		}
	}
	if err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil, &info); err != nil {
		return false, err
	}
	h := info.Config.Healthcheck
	return h != nil && len(h.Test) > 0 && h.Test[0] != "NONE", nil
}

// RepoDigests returns an image's registry digests ("repo@sha256:…"), which
// docker records when it pulls. Locally built images have none.
func (c *Client) RepoDigests(ctx context.Context, image string) ([]string, error) {
	var info struct{ RepoDigests []string }
	if err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil, &info); err != nil {
		return nil, err
	}
	return info.RepoDigests, nil
}

// OCI labels an image may carry about where it was built from.
const (
	LabelSource   = "org.opencontainers.image.source"
	LabelRevision = "org.opencontainers.image.revision"
)

// Image is a local image as a deploy record needs it.
type Image struct {
	ID string
	// RepoDigests are the registry digests ("repo@sha256:…") docker
	// recorded when it pulled the image; none for locally built ones.
	RepoDigests []string
	Labels      map[string]string
}

// Image inspects a local image by ID or reference; ErrNotFound means it
// isn't on this host.
func (c *Client) Image(ctx context.Context, ref string) (*Image, error) {
	var info struct {
		ID          string `json:"Id"`
		RepoDigests []string
		Config      struct{ Labels map[string]string }
	}
	if err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, &info); err != nil {
		return nil, err
	}
	return &Image{ID: info.ID, RepoDigests: info.RepoDigests, Labels: info.Config.Labels}, nil
}

// Tag adds repo:tag to a local image.
func (c *Client) Tag(ctx context.Context, image, repo, tag string) error {
	q := url.Values{"repo": {repo}, "tag": {tag}}
	return c.do(ctx, http.MethodPost, "/images/"+url.PathEscape(image)+"/tag", q, nil, nil)
}

// Untag removes a repo:tag. The image itself goes too when nothing else
// refers to it, as with `docker rmi`; one a container uses stays.
func (c *Client) Untag(ctx context.Context, ref string) error {
	err := c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(ref), url.Values{"force": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Tagged lists the local image tags under repo, e.g. hoist-keep/main-stack.
func (c *Client) Tagged(ctx context.Context, repo string) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"reference": {repo}})
	var items []struct{ RepoTags []string }
	if err := c.do(ctx, http.MethodGet, "/images/json", url.Values{"filters": {string(filters)}}, nil, &items); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range items {
		for _, t := range it.RepoTags {
			if strings.HasPrefix(t, repo+":") {
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Health is a container's state, as far as judging a deploy goes.
type Health struct {
	Running bool
	// Status is the healthcheck's: healthy, unhealthy, starting, or ""
	// without one.
	Status    string
	StartedAt time.Time
}

// Health inspects a container; ErrNotFound when it's gone.
func (c *Client) Health(ctx context.Context, id string) (*Health, error) {
	var info struct {
		State struct {
			Running   bool
			StartedAt time.Time
			Health    *struct{ Status string }
		}
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &info); err != nil {
		return nil, err
	}
	h := &Health{Running: info.State.Running, StartedAt: info.State.StartedAt}
	if info.State.Health != nil {
		h.Status = info.State.Health.Status
	}
	return h, nil
}
