// Package compose runs docker compose for a stack and works out what a
// deploy would change.
package compose

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
)

// Bin is the compose binary. The standalone docker-compose binary is the
// same program as the `docker compose` plugin, so the image doesn't need the
// docker CLI.
var Bin = "docker-compose"

// baseArgs pins the project, file and folder, so the result matches what
// was deployed before (and the containers' labels).
func baseArgs(s config.Stack, file string) []string {
	return []string{"--project-name", s.Project, "--file", file, "--project-directory", s.Path, "--ansi", "never"}
}

// environ is what compose sees besides .env. Compose lets these override
// .env when it interpolates ${VARS}, so only what compose itself needs goes
// in: not Hoist's token, and not its TZ (a stack's .env may set another).
// HOME is the host user's (HOIST_HOST_HOME), so `~/` in a compose file
// resolves to the same folder as when the stack was deployed from a shell.
func environ() []string {
	home := os.Getenv("HOIST_HOST_HOME")
	if home == "" {
		home = os.Getenv("HOME")
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	for _, k := range []string{"DOCKER_HOST", "DOCKER_CONFIG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func command(ctx context.Context, s config.Stack, file string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, Bin, append(baseArgs(s, file), args...)...)
	cmd.Dir = s.Path
	cmd.Env = environ()
	return cmd
}

func output(ctx context.Context, s config.Stack, file string, args ...string) ([]byte, error) {
	cmd := command(ctx, s, file, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return stdout.Bytes(), nil
}

// Run runs a compose command for the stack, streaming its output to w.
func Run(ctx context.Context, s config.Stack, w io.Writer, args ...string) error {
	cmd := command(ctx, s, s.ComposePath(), append([]string{"--progress", "plain"}, args...)...)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// Validate checks content as the stack's compose file, without touching the
// real one. The check runs in the stack folder, so .env, env_file and
// relative paths resolve as they would on deploy.
func Validate(ctx context.Context, s config.Stack, content []byte) error {
	_, err := withTemp(ctx, s, content, "config", "--quiet")
	return err
}

// Resolve validates content like Validate and returns its services.
func Resolve(ctx context.Context, s config.Stack, content []byte) ([]Service, error) {
	data, err := withTemp(ctx, s, content, "config", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseServices(data)
}

// withTemp runs compose on content saved as a temporary file in the stack
// folder.
func withTemp(ctx context.Context, s config.Stack, content []byte, args ...string) ([]byte, error) {
	tmp, err := os.CreateTemp(s.Path, ".hoist-validate-*.yml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	out, err := output(ctx, s, tmp.Name(), args...)
	if err != nil {
		return nil, cleanError(err, tmp.Name(), s.File)
	}
	return out, nil
}

// cleanError names the real compose file instead of the temp copy.
func cleanError(err error, tmp, name string) error {
	msg := strings.ReplaceAll(err.Error(), tmp, name)
	msg = strings.ReplaceAll(msg, filepath.Base(tmp), name)
	return fmt.Errorf("%s", msg)
}

// Service is one service as compose resolves it.
type Service struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	ContainerName string `json:"container_name,omitempty"`
	Hash          string `json:"-"`
	// Ports are the host ports it publishes (ranges come expanded).
	Ports []Port   `json:"-"`
	Lint  LintInfo `json:"-"`
}

// LintInfo is what Lint looks at in a resolved service.
type LintInfo struct {
	Build          bool   // built from source, not pulled
	Restart        string // restart, or deploy.restart_policy.condition
	Healthcheck    bool
	HealthcheckOff bool // healthcheck: disable: true
}

// Port is a published host port.
type Port struct {
	HostIP   string // "" for every address
	Port     int
	Protocol string // tcp | udp
}

func (p Port) String() string {
	s := strconv.Itoa(p.Port) + "/" + p.Protocol
	if p.HostIP != "" && p.HostIP != "0.0.0.0" && p.HostIP != "::" {
		s = p.HostIP + ":" + s
	}
	return s
}

// Overlaps reports whether two published ports would clash on the host.
func (p Port) Overlaps(q Port) bool {
	all := func(ip string) bool { return ip == "" || ip == "0.0.0.0" || ip == "::" }
	return p.Port == q.Port && p.Protocol == q.Protocol && (all(p.HostIP) || all(q.HostIP) || p.HostIP == q.HostIP)
}

func parseServices(data []byte) ([]Service, error) {
	var model struct {
		Services map[string]struct {
			Image         string `json:"image"`
			ContainerName string `json:"container_name"`
			Ports         []struct {
				HostIP    string `json:"host_ip"`
				Published string `json:"published"`
				Protocol  string `json:"protocol"`
			} `json:"ports"`
			Build       json.RawMessage `json:"build"`
			Restart     string          `json:"restart"`
			Healthcheck *struct {
				Test    []string `json:"test"`
				Disable bool     `json:"disable"`
			} `json:"healthcheck"`
			Deploy *struct {
				RestartPolicy *struct {
					Condition string `json:"condition"`
				} `json:"restart_policy"`
			} `json:"deploy"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(model.Services))
	for name, svc := range model.Services {
		s := Service{Name: name, Image: svc.Image, ContainerName: svc.ContainerName}
		s.Lint.Build = len(svc.Build) > 0 && string(svc.Build) != "null"
		s.Lint.Restart = svc.Restart
		if d := svc.Deploy; s.Lint.Restart == "" && d != nil && d.RestartPolicy != nil {
			s.Lint.Restart = d.RestartPolicy.Condition
			if s.Lint.Restart == "" {
				s.Lint.Restart = "any"
			}
		}
		if h := svc.Healthcheck; h != nil {
			off := h.Disable || (len(h.Test) > 0 && h.Test[0] == "NONE")
			s.Lint.HealthcheckOff = off
			s.Lint.Healthcheck = !off && len(h.Test) > 0
		}
		for _, p := range svc.Ports {
			n, err := strconv.Atoi(p.Published)
			if err != nil || n == 0 { // not published, or left to docker
				continue
			}
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			s.Ports = append(s.Ports, Port{HostIP: p.HostIP, Port: n, Protocol: proto})
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Services resolves the compose file: every service, its image, its ports,
// and the config hash compose would label a fresh container with.
func Services(ctx context.Context, s config.Stack) ([]Service, error) {
	data, err := output(ctx, s, s.ComposePath(), "config", "--format", "json")
	if err != nil {
		return nil, err
	}
	out, err := parseServices(data)
	if err != nil {
		return nil, err
	}
	hashes, err := output(ctx, s, s.ComposePath(), "config", "--hash", "*")
	if err != nil {
		return nil, err
	}
	byName := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(hashes))
	for sc.Scan() {
		if name, hash, ok := strings.Cut(strings.TrimSpace(sc.Text()), " "); ok {
			byName[name] = strings.TrimSpace(hash)
		}
	}
	for i := range out {
		out[i].Hash = byName[out[i].Name]
	}
	return out, nil
}

// Change is what `up` would do to a service.
type Change string

const (
	Unchanged Change = ""
	Create    Change = "create"   // no container yet
	Recreate  Change = "recreate" // the config changed
	Start     Change = "start"    // exists but isn't running
	Remove    Change = "remove"   // gone from the file; removed as an orphan
)

// ServiceState joins a service with its container and the pending change.
type ServiceState struct {
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Change    Change            `json:"change,omitempty"`
	Container *docker.Container `json:"container,omitempty"`
	// Orphan is a container whose service is no longer in the file.
	Orphan bool `json:"orphan,omitempty"`
}

// Plan compares the resolved services with the running containers. It can't
// see new images behind the same tag; a deploy pulls first and may recreate
// more.
func Plan(services []Service, containers []docker.Container, removeOrphans bool) []ServiceState {
	byService := map[string]*docker.Container{}
	for i := range containers {
		c := &containers[i]
		if c.OneOff {
			continue
		}
		// With scale > 1 any replica will do for the state.
		if prev, ok := byService[c.Service]; !ok || (prev.State != "running" && c.State == "running") {
			byService[c.Service] = c
		}
	}
	out := []ServiceState{}
	known := map[string]bool{}
	for _, svc := range services {
		known[svc.Name] = true
		st := ServiceState{Name: svc.Name, Image: svc.Image, Container: byService[svc.Name]}
		switch c := st.Container; {
		case c == nil:
			st.Change = Create
		case svc.Hash != "" && c.ConfigHash != svc.Hash:
			st.Change = Recreate
		case c.State != "running" && c.State != "restarting":
			st.Change = Start
		}
		out = append(out, st)
	}
	var orphans []string
	for name := range byService {
		if !known[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	for _, name := range orphans {
		c := byService[name]
		st := ServiceState{Name: name, Image: c.Image, Container: c, Orphan: true}
		if removeOrphans {
			st.Change = Remove
		}
		out = append(out, st)
	}
	return out
}
