// Package config loads hoist.yaml: the stacks Hoist manages and the git
// identity it commits with.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Git struct {
	Name  string `yaml:"name" json:"name"`
	Email string `yaml:"email" json:"email"`
	// Push after every commit. On by default.
	Push *bool `yaml:"push,omitempty" json:"push"`
	// Conventional refuses commit messages that don't follow Conventional
	// Commits. On by default.
	Conventional *bool `yaml:"conventional,omitempty" json:"conventional"`
}

func (g Git) ShouldPush() bool { return g.Push == nil || *g.Push }

func (g Git) EnforceConventional() bool { return g.Conventional == nil || *g.Conventional }

type Stack struct {
	Name string `yaml:"name" json:"name"`
	// Path is the stack's folder on the host. Hoist must see it at the same
	// path, because compose resolves relative bind mounts against it.
	Path string `yaml:"path" json:"path"`
	// File is the compose file inside Path; found automatically if empty.
	File string `yaml:"file,omitempty" json:"file"`
	// Project is the compose project name. It must match the running
	// containers' com.docker.compose.project label, or compose treats them
	// as someone else's. Defaults to the folder name, like compose does.
	Project string `yaml:"project,omitempty" json:"project"`
	// RemoveOrphans stops containers whose service was removed from the
	// file. On by default.
	RemoveOrphans *bool `yaml:"remove_orphans,omitempty" json:"remove_orphans"`
}

func (s Stack) ShouldRemoveOrphans() bool { return s.RemoveOrphans == nil || *s.RemoveOrphans }

// ComposePath is the absolute path of the compose file.
func (s Stack) ComposePath() string { return filepath.Join(s.Path, s.File) }

// EnvPath is the .env file compose reads for interpolation.
func (s Stack) EnvPath() string { return filepath.Join(s.Path, ".env") }

type Config struct {
	Git    Git     `yaml:"git" json:"git"`
	Stacks []Stack `yaml:"stacks" json:"stacks"`
}

func (c *Config) Stack(name string) (Stack, bool) {
	for _, s := range c.Stacks {
		if s.Name == name {
			return s, true
		}
	}
	return Stack{}, false
}

// composeFiles are the names compose looks for, in its order.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	projectRe = regexp.MustCompile(`[^a-z0-9_-]`)
)

// ProjectName normalises a folder name the way compose does.
func ProjectName(dir string) string {
	return strings.TrimLeft(projectRe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), ""), "_-")
}

// Load reads and checks the config, filling in defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.normalise(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) normalise() error {
	if c.Git.Name == "" {
		c.Git.Name = "Hoist"
	}
	if c.Git.Email == "" {
		c.Git.Email = "hoist@localhost"
	}
	if len(c.Stacks) == 0 {
		return errors.New("no stacks configured")
	}
	seen := map[string]bool{}
	for i := range c.Stacks {
		s := &c.Stacks[i]
		if !nameRe.MatchString(s.Name) {
			return fmt.Errorf("stack %q: name must be lowercase letters, digits, - and _", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("stack %q is listed twice", s.Name)
		}
		seen[s.Name] = true
		if !filepath.IsAbs(s.Path) {
			return fmt.Errorf("stack %q: path must be absolute (the same path as on the host)", s.Name)
		}
		s.Path = filepath.Clean(s.Path)
		if s.File == "" {
			s.File = findComposeFile(s.Path)
		}
		if s.File == "" {
			return fmt.Errorf("stack %q: no compose file in %s", s.Name, s.Path)
		}
		if strings.Contains(s.File, "/") || strings.HasPrefix(s.File, ".") {
			return fmt.Errorf("stack %q: file must be a file name inside the stack folder", s.Name)
		}
		if s.Project == "" {
			s.Project = ProjectName(s.Path)
		}
		if projectRe.MatchString(s.Project) {
			return fmt.Errorf("stack %q: project %q must be lowercase letters, digits, - and _", s.Name, s.Project)
		}
	}
	return nil
}

func findComposeFile(dir string) string {
	for _, name := range composeFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}
