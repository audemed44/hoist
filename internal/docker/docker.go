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
}

const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
	labelHash    = "com.docker.compose.config-hash"
	labelOneOff  = "com.docker.compose.oneoff"
)

// Project lists a compose project's containers, stopped ones included.
func (c *Client) Project(ctx context.Context, project string) ([]Container, error) {
	var items []struct {
		ID      string
		Names   []string
		Image   string
		ImageID string
		State   string
		Status  string
		Created int64
		Labels  map[string]string
	}
	filters, _ := json.Marshal(map[string][]string{"label": {labelProject + "=" + project}})
	q := url.Values{"all": {"1"}, "filters": {string(filters)}}
	if err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &items); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(items))
	for _, it := range items {
		name := it.ID[:min(12, len(it.ID))]
		if len(it.Names) > 0 {
			name = strings.TrimPrefix(it.Names[0], "/")
		}
		out = append(out, Container{
			ID: it.ID, Name: name, Service: it.Labels[labelService], Image: it.Image, ImageID: it.ImageID,
			State: it.State, Status: it.Status, Created: it.Created,
			ConfigHash: it.Labels[labelHash], OneOff: it.Labels[labelOneOff] == "True",
		})
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

// RepoDigests returns an image's registry digests ("repo@sha256:…"), which
// docker records when it pulls. Locally built images have none.
func (c *Client) RepoDigests(ctx context.Context, image string) ([]string, error) {
	var info struct{ RepoDigests []string }
	if err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil, &info); err != nil {
		return nil, err
	}
	return info.RepoDigests, nil
}
