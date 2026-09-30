package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
)

// conflict is a host port or container name that a service of an edited
// compose file shares with something else on the host.
type conflict struct {
	Service string `json:"service"`
	Kind    string `json:"kind"` // port | container_name
	What    string `json:"what"` // e.g. 8080/tcp, or the name
	With    string `json:"with"` // who else has it
}

func (c conflict) String() string {
	return fmt.Sprintf("%s: %s %s is also used by %s", c.Service, strings.ReplaceAll(c.Kind, "_", " "), c.What, c.With)
}

// conflicts compares svcs, the services of st's edited compose file, with
// the file itself, the other stacks' files, and the containers on the host
// that no stack covers. Ports only clash on the same address; container
// names clash anywhere.
func (s *Server) conflicts(ctx context.Context, st config.Stack, svcs []compose.Service) []conflict {
	out := []conflict{}
	seen := map[conflict]bool{}
	add := func(c conflict) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for i, a := range svcs {
		for _, b := range svcs[i+1:] {
			for _, p := range a.Ports {
				for _, q := range b.Ports {
					if p.Overlaps(q) {
						add(conflict{Service: a.Name, Kind: "port", What: p.String(), With: "service " + b.Name + " in this file"})
					}
				}
			}
		}
	}

	managed := map[string]bool{st.Project: true}
	for _, other := range s.Config.List() {
		managed[other.Project] = true
		if other.Name == st.Name {
			continue
		}
		theirs, err := s.services(ctx, other)
		if err != nil {
			continue
		}
		for _, a := range svcs {
			for _, b := range theirs {
				with := "stack " + other.Name + ", service " + b.Name
				for _, p := range a.Ports {
					for _, q := range b.Ports {
						if p.Overlaps(q) {
							add(conflict{Service: a.Name, Kind: "port", What: p.String(), With: with})
						}
					}
				}
				if a.ContainerName != "" && a.ContainerName == b.ContainerName {
					add(conflict{Service: a.Name, Kind: "container_name", What: a.ContainerName, With: with})
				}
			}
		}
	}

	containers, err := s.Docker.Containers(ctx)
	if err != nil {
		return out
	}
	for _, c := range containers {
		if managed[c.Project] {
			continue
		}
		with := "container " + c.Name
		if c.Project != "" {
			with += " (compose project " + c.Project + ", not in Hoist)"
		}
		for _, a := range svcs {
			if a.ContainerName != "" && a.ContainerName == c.Name {
				add(conflict{Service: a.Name, Kind: "container_name", What: a.ContainerName, With: with})
			}
			if c.State != "running" {
				continue
			}
			for _, p := range a.Ports {
				for _, q := range c.Ports {
					if p.Overlaps(compose.Port{HostIP: q.IP, Port: q.Port, Protocol: q.Protocol}) {
						add(conflict{Service: a.Name, Kind: "port", What: p.String(), With: with})
					}
				}
			}
		}
	}
	return out
}
