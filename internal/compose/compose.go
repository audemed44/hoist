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

// environ is what compose sees besides .env. Hoist's own settings (the API
// token) stay out, so a compose file can't interpolate them.
func environ() []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	for _, k := range []string{"DOCKER_HOST", "DOCKER_CONFIG", "TZ"} {
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
	tmp, err := os.CreateTemp(s.Path, ".hoist-validate-*.yml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := output(ctx, s, tmp.Name(), "config", "--quiet"); err != nil {
		return cleanError(err, tmp.Name(), s.File)
	}
	return nil
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
}

// Services resolves the compose file: every service, its image, and the
// config hash compose would label a fresh container with.
func Services(ctx context.Context, s config.Stack) ([]Service, error) {
	data, err := output(ctx, s, s.ComposePath(), "config", "--format", "json")
	if err != nil {
		return nil, err
	}
	var model struct {
		Services map[string]struct {
			Image         string `json:"image"`
			ContainerName string `json:"container_name"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &model); err != nil {
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
	out := make([]Service, 0, len(model.Services))
	for name, svc := range model.Services {
		out = append(out, Service{Name: name, Image: svc.Image, ContainerName: svc.ContainerName, Hash: byName[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
