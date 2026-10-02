// Package config loads hoist.yaml: the stacks Hoist manages and the git
// identity it commits with.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

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
	// Updates overrides the automatic update policy.
	Updates *StackUpdates `yaml:"updates,omitempty" json:"updates,omitempty"`
	// Pin is set while the stack is rolled back to an earlier deploy.
	Pin *Pin `yaml:"-" json:"-"`
}

// Pin is a stack rolled back to an earlier deploy. Compose runs with an
// override that pins every image by digest, so a deploy doesn't drift back
// to :latest, and, when that deploy used an older compose file, with a copy
// of that file instead of the stack's. Both live in the config folder, not
// in the stack's repo.
type Pin struct {
	// Deploy is the job rolled back to.
	Deploy string    `json:"deploy"`
	At     time.Time `json:"at"`
	// Commit is the compose file's commit that deploy used.
	Commit string `json:"commit,omitempty"`
	// Base is the copy of the older compose file; "" uses the stack's.
	Base     string `json:"base,omitempty"`
	Override string `json:"override"`
	// Images are the pinned references, by service.
	Images map[string]string `json:"images"`
}

// Files are the compose files a deploy uses: the stack's, or while it's
// pinned, the base and the override.
func (s Stack) Files() []string {
	if s.Pin == nil {
		return []string{s.ComposePath()}
	}
	base := s.ComposePath()
	if s.Pin.Base != "" {
		base = s.Pin.Base
	}
	return []string{base, s.Pin.Override}
}

func (s Stack) ShouldRemoveOrphans() bool { return s.RemoveOrphans == nil || *s.RemoveOrphans }

// ComposePath is the absolute path of the compose file.
func (s Stack) ComposePath() string { return filepath.Join(s.Path, s.File) }

// EnvPath is the .env file compose reads for interpolation.
func (s Stack) EnvPath() string { return filepath.Join(s.Path, ".env") }

// Updates configures the image update check.
type Updates struct {
	// Every is how often to check registries, e.g. "6h"; "off" checks only
	// when asked. Default 6h.
	Every string `yaml:"every,omitempty" json:"every"`
	// Auto is the default policy: off (report only), digest (redeploy when
	// a tag gets a new image), patch or minor (also bump pinned versions
	// that far). Majors are never automatic. Default off.
	Auto string `yaml:"auto,omitempty" json:"auto"`
	// Notify is an Apprise API URL (…/notify/<key>) told about automatic
	// updates and failures.
	Notify string `yaml:"notify,omitempty" json:"-"`

	interval time.Duration
}

// Interval is the check period; 0 means only on demand.
func (u Updates) Interval() time.Duration { return u.interval }

// StackUpdates overrides the update policy for a stack and its services.
type StackUpdates struct {
	Auto     string            `yaml:"auto,omitempty" json:"auto"`
	Services map[string]string `yaml:"services,omitempty" json:"services"`
}

// Rollback configures what counts as a good deploy to roll back to.
type Rollback struct {
	// HealthyFor is how long a deploy's containers must run, without
	// restarting or failing a healthcheck, for it to count as good.
	// Default 5m.
	HealthyFor string `yaml:"healthy_for,omitempty" json:"healthy_for"`
	// Keep is how many good deploys per stack keep their images on the
	// host (tagged hoist-keep/<stack>), so a prune can't remove them.
	// Default 2.
	Keep int `yaml:"keep,omitempty" json:"keep"`

	healthyFor time.Duration
}

// Healthy is HealthyFor as a duration.
func (r Rollback) Healthy() time.Duration { return r.healthyFor }

var policies = map[string]bool{"off": true, "digest": true, "patch": true, "minor": true}

type Config struct {
	Git      Git      `yaml:"git" json:"git"`
	Updates  Updates  `yaml:"updates" json:"updates"`
	Rollback Rollback `yaml:"rollback" json:"rollback"`
	// Stacks is read through List and Stack once the server runs, since
	// AddStack can change it.
	Stacks []Stack `yaml:"stacks" json:"stacks"`

	mu   sync.RWMutex
	path string // where it was loaded from
}

// Policy is the update policy for one service of a stack.
func (c *Config) Policy(st Stack, service string) string {
	if st.Updates != nil {
		if p := st.Updates.Services[service]; p != "" {
			return p
		}
		if st.Updates.Auto != "" {
			return st.Updates.Auto
		}
	}
	return c.Updates.Auto
}

// List returns the stacks in config order.
func (c *Config) List() []Stack {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.Stacks)
}

func (c *Config) Stack(name string) (Stack, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
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
	cfg.path = path
	cfg.loadPins()
	return &cfg, nil
}

// PinDir is where a stack's pin files go.
func (c *Config) PinDir(stack string) string {
	return filepath.Join(filepath.Dir(c.path), "pins", stack)
}

func (c *Config) loadPins() {
	for i := range c.Stacks {
		data, err := os.ReadFile(filepath.Join(c.PinDir(c.Stacks[i].Name), "pin.json"))
		if err != nil {
			continue
		}
		var p Pin
		if json.Unmarshal(data, &p) == nil && p.Override != "" {
			c.Stacks[i].Pin = &p
		}
	}
}

// SetPin pins a stack, or unpins it (nil). The pin is saved, so the
// self-deploy helper and a restarted Hoist see it. The files it points to
// are the caller's; PrunePins removes the ones no longer used.
func (c *Config) SetPin(stack string, p *Pin) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.Stacks, func(s Stack) bool { return s.Name == stack })
	if i < 0 {
		return fmt.Errorf("no stack %q", stack)
	}
	dir := c.PinDir(stack)
	if p == nil {
		c.Stacks[i].Pin = nil
		if err := os.Remove(filepath.Join(dir, "pin.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "pin.json"), data, 0o644); err != nil {
		return err
	}
	c.Stacks[i].Pin = p
	return nil
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
	switch c.Updates.Every {
	case "":
		c.Updates.interval = 6 * time.Hour
	case "off", "0":
		c.Updates.interval = 0
	default:
		d, err := time.ParseDuration(c.Updates.Every)
		if err != nil || d < 10*time.Minute {
			return fmt.Errorf("updates.every: %q should be like 6h (at least 10m), or off", c.Updates.Every)
		}
		c.Updates.interval = d
	}
	if c.Updates.Auto == "" {
		c.Updates.Auto = "off"
	}
	if !policies[c.Updates.Auto] {
		return fmt.Errorf("updates.auto: %q should be off, digest, patch or minor", c.Updates.Auto)
	}
	c.Rollback.healthyFor = 5 * time.Minute
	if h := c.Rollback.HealthyFor; h != "" {
		d, err := time.ParseDuration(h)
		if err != nil || d < 0 {
			return fmt.Errorf("rollback.healthy_for: %q should be like 5m", h)
		}
		c.Rollback.healthyFor = d
	}
	if c.Rollback.Keep == 0 {
		c.Rollback.Keep = 2
	}
	if c.Rollback.Keep < 0 {
		return errors.New("rollback.keep can't be negative")
	}
	seen := map[string]bool{}
	for i := range c.Stacks {
		s := &c.Stacks[i]
		if err := s.normalise(); err != nil {
			return err
		}
		if seen[s.Name] {
			return fmt.Errorf("stack %q is listed twice", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

func (s *Stack) normalise() error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("stack %q: name must be lowercase letters, digits, - and _", s.Name)
	}
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
	if u := s.Updates; u != nil {
		if u.Auto != "" && !policies[u.Auto] {
			return fmt.Errorf("stack %q: updates.auto %q should be off, digest, patch or minor", s.Name, u.Auto)
		}
		for svc, p := range u.Services {
			if !policies[p] {
				return fmt.Errorf("stack %q: updates for %s: %q should be off, digest, patch or minor", s.Name, svc, p)
			}
		}
	}
	return nil
}

// ValidName reports whether name can be a stack's name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// DefaultFile is the compose file name compose would pick in dir, or "".
func DefaultFile(dir string) string { return findComposeFile(dir) }

// AddStack checks st, appends it to hoist.yaml (keeping the file's comments
// and layout) and to the running config.
func (c *Config) AddStack(st Stack) (Stack, error) {
	if err := st.normalise(); err != nil {
		return st, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.Stacks {
		if s.Name == st.Name {
			return st, fmt.Errorf("there is already a stack named %s", st.Name)
		}
		if s.Project == st.Project {
			return st, fmt.Errorf("stack %s already uses the compose project %s", s.Name, st.Project)
		}
		if s.Path == st.Path && s.File == st.File {
			return st, fmt.Errorf("stack %s already uses %s", s.Name, st.ComposePath())
		}
	}
	if c.path != "" {
		if err := appendStack(c.path, st); err != nil {
			return st, err
		}
	}
	c.Stacks = append(c.Stacks, st)
	return st, nil
}

// appendStack adds st to the stacks list in the file at path, as new lines
// after the list's last entry, so the rest of the file stays as it was. Only
// name, path, project and a file compose wouldn't find itself are written.
func appendStack(path string, st Stack) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a mapping at the top", path)
	}
	root := doc.Content[0]
	var list, next *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "stacks" {
			list = root.Content[i+1]
			if i+2 < len(root.Content) {
				next = root.Content[i+2]
			}
		}
	}
	if list == nil || list.Kind != yaml.SequenceNode || len(list.Content) == 0 || list.Style&yaml.FlowStyle != 0 {
		return fmt.Errorf("%s: stacks isn't a list Hoist can add to; add %s by hand", path, st.Name)
	}
	lines := strings.SplitAfter(string(data), "\n")
	first := list.Content[0]
	dash := strings.Index(lines[first.Line-1], "-")
	if dash < 0 || first.Column-1 <= dash {
		return fmt.Errorf("%s: unexpected layout of the stacks list; add %s by hand", path, st.Name)
	}
	indent := strings.Repeat(" ", first.Column-1)

	entry := [][2]string{{"name", st.Name}, {"path", st.Path}}
	if findComposeFile(st.Path) != st.File {
		entry = append(entry, [2]string{"file", st.File})
	}
	entry = append(entry, [2]string{"project", st.Project})
	var add strings.Builder
	for i, kv := range entry {
		v, err := yaml.Marshal(kv[1])
		if err != nil {
			return err
		}
		prefix := indent
		if i == 0 {
			prefix = strings.Repeat(" ", dash) + "-" + strings.Repeat(" ", first.Column-2-dash)
		}
		add.WriteString(prefix + kv[0] + ": " + strings.TrimSpace(string(v)) + "\n")
	}

	// Insert before the next top-level key and the comments and blank lines
	// above it, or at the end of the file.
	at := len(lines)
	if next != nil {
		at = next.Line - 1
		for at > 0 {
			t := strings.TrimSpace(lines[at-1])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			at--
		}
	} else {
		for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
	}
	var out strings.Builder
	for _, l := range lines[:at] {
		out.WriteString(l)
	}
	if at > 0 && !strings.HasSuffix(lines[at-1], "\n") {
		out.WriteString("\n")
	}
	out.WriteString(add.String())
	for _, l := range lines[at:] {
		out.WriteString(l)
	}

	var check Config
	if err := yaml.Unmarshal([]byte(out.String()), &check); err != nil {
		return fmt.Errorf("the updated config doesn't parse: %w", err)
	}
	if n := len(check.Stacks); n != len(list.Content)+1 || check.Stacks[n-1].Name != st.Name || check.Stacks[n-1].Path != st.Path {
		return fmt.Errorf("%s: couldn't add %s cleanly; add it by hand", path, st.Name)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return writeAtomic(path, []byte(out.String()), info.Mode().Perm())
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func findComposeFile(dir string) string {
	for _, name := range composeFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}

// PrunePins removes a stack's pin files other than its current pin's.
func (c *Config) PrunePins(stack string) {
	st, ok := c.Stack(stack)
	if !ok {
		return
	}
	dir := c.PinDir(stack)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.Name() == "pin.json" || (st.Pin != nil && strings.HasPrefix(st.Pin.Override, path+string(filepath.Separator))) {
			continue
		}
		os.RemoveAll(path)
	}
	if st.Pin == nil {
		os.Remove(dir)
	}
}
