package server

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/registry"
)

type suggestedPort struct {
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`
	Host      int    `json:"host"` // a free host port
}

type suggestedVolume struct {
	Container string `json:"container"`
	Host      string `json:"host"`
}

// serviceSuggestion is a service to add for an image: a free name, a
// pinned tag, and the image's ports and volumes mapped to free host ports
// and folders next to the compose file.
type serviceSuggestion struct {
	Service     string            `json:"service"`
	Image       string            `json:"image"`
	Tags        []string          `json:"tags"` // recent version tags to pick from
	Ports       []suggestedPort   `json:"ports"`
	Volumes     []suggestedVolume `json:"volumes"`
	Healthcheck bool              `json:"healthcheck"` // the image has one
	// Note says what couldn't be looked up.
	Note string `json:"note,omitempty"`
}

// suggestService looks an image up in its registry, without pulling it,
// and suggests how to add it to the stack's (edited) compose file.
func (s *Server) suggestService(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	var body struct {
		Image   string `json:"image"`
		Content string `json:"content"`
	}
	if !readJSON(w, r, maxCompose+(4<<10), &body) {
		return
	}
	ref, err := registry.Parse(strings.TrimSpace(body.Image))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	name := strings.TrimSpace(body.Image)
	if i := strings.IndexAny(name, "@"); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	out := serviceSuggestion{Image: name, Tags: []string{}, Ports: []suggestedPort{}, Volumes: []suggestedVolume{}}

	explicit := strings.Contains(path.Base(strings.TrimSpace(body.Image)), ":") || ref.Digest != ""
	if explicit {
		out.Image = strings.TrimSpace(body.Image)
	}
	if tags, err := s.registry.Tags(ctx, ref); err != nil {
		out.Note = "Couldn't list tags: " + err.Error()
	} else {
		out.Tags = recentVersions(tags, 12)
		if pin := registry.Pinnable(tags); pin != "" && !explicit {
			out.Image = name + ":" + pin
			ref.Tag = pin
		}
	}
	if !explicit && ref.Tag == "latest" {
		out.Image = name + ":latest"
	}

	svcs, _ := compose.Resolve(ctx, st, []byte(body.Content))
	out.Service = freeName(serviceName(ref.Repo), svcs)

	cfg, err := s.registry.Config(ctx, ref)
	if err != nil {
		out.Note = strings.TrimSpace(out.Note + " Couldn't read the image's config: " + err.Error())
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Healthcheck = cfg.Healthcheck
	taken := s.takenPorts(ctx, st, svcs)
	for _, p := range cfg.Ports {
		num, proto, _ := strings.Cut(p, "/")
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		if proto == "" {
			proto = "tcp"
		}
		host := freePort(n, proto, taken)
		taken[portKey(host, proto)] = true
		out.Ports = append(out.Ports, suggestedPort{Container: n, Protocol: proto, Host: host})
	}
	for _, v := range cfg.Volumes {
		out.Volumes = append(out.Volumes, suggestedVolume{Container: v, Host: "./" + out.Service + "/" + path.Base(v)})
	}
	writeJSON(w, http.StatusOK, out)
}

func portKey(n int, proto string) string { return strconv.Itoa(n) + "/" + proto }

// takenPorts are the host ports published by any stack's file, the edited
// file, or a running container.
func (s *Server) takenPorts(ctx context.Context, st config.Stack, draft []compose.Service) map[string]bool {
	taken := map[string]bool{}
	add := func(svcs []compose.Service) {
		for _, svc := range svcs {
			for _, p := range svc.Ports {
				taken[portKey(p.Port, p.Protocol)] = true
			}
		}
	}
	add(draft)
	for _, other := range s.Config.List() {
		if other.Name == st.Name && draft != nil {
			continue
		}
		if svcs, err := s.services(ctx, other); err == nil {
			add(svcs)
		}
	}
	if containers, err := s.Docker.Containers(ctx); err == nil {
		for _, c := range containers {
			if c.State != "running" {
				continue
			}
			for _, p := range c.Ports {
				taken[portKey(p.Port, p.Protocol)] = true
			}
		}
	}
	return taken
}

// freePort keeps a container port of 1024 and up, maps a privileged one to
// 8000 + it (80 → 8080, 443 → 8443), and counts up from there to a free one.
func freePort(container int, proto string, taken map[string]bool) int {
	p := container
	if p < 1024 {
		p = 8000 + p
	}
	for taken[portKey(p, proto)] && p < 65535 {
		p++
	}
	return p
}

// serviceName makes a service name of a repository: "linuxserver/sonarr" is
// sonarr.
func serviceName(repo string) string {
	n := strings.ToLower(path.Base(repo))
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '.':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "app"
	}
	return b.String()
}

func freeName(base string, svcs []compose.Service) string {
	used := map[string]bool{}
	for _, s := range svcs {
		used[s.Name] = true
	}
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// recentVersions returns up to n of the newest version tags, newest first,
// in the shape Pinnable chose.
func recentVersions(tags []string, n int) []string {
	pin, ok := registry.ParseVersion(registry.Pinnable(tags))
	if !ok {
		return []string{}
	}
	out := []string{pin.Tag}
	for _, v := range registry.Older(pin, tags) {
		if len(out) == n {
			break
		}
		out = append(out, v.Tag)
	}
	return out
}
