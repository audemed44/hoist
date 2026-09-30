// Package jobs records deploys: one JSON file and one log file each, on disk,
// so a deploy run by the self-deploy helper container shows up in the Hoist
// that replaces the one that started it.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type State string

const (
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
)

type Job struct {
	ID       string     `json:"id"`
	Stack    string     `json:"stack"`
	Services []string   `json:"services,omitempty"` // empty: the whole stack
	State    State      `json:"state"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	// Trigger is where the deploy came from: "ui", "api", "foyer" or "auto".
	Trigger string `json:"trigger"`
	// Self is set when Hoist deployed its own stack through the helper.
	Self   bool   `json:"self,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Dirty is set when the compose file had uncommitted changes, so what
	// was deployed isn't exactly Commit.
	Dirty  bool    `json:"dirty,omitempty"`
	Result *Result `json:"result,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// Result is what the deploy changed, by service.
type Result struct {
	Created   []string `json:"created"`
	Recreated []string `json:"recreated"`
	Removed   []string `json:"removed"`
	Started   []string `json:"started"`
	// Updated are the recreated services now running a different image.
	Updated []string `json:"updated"`
}

// Summary is a one-line description, e.g. "Recreated foyer, shelfloom".
func (r *Result) Summary() string {
	if r == nil {
		return ""
	}
	var parts []string
	add := func(verb string, names []string) {
		if len(names) > 0 {
			parts = append(parts, verb+" "+strings.Join(names, ", "))
		}
	}
	add("created", r.Created)
	add("recreated", r.Recreated)
	add("started", r.Started)
	add("removed", r.Removed)
	if len(parts) == 0 {
		return "Nothing changed"
	}
	s := strings.Join(parts, "; ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// keep is how many jobs stay on disk.
const keep = 100

var idRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)

// ValidID reports whether id looks like one Hoist made.
func ValidID(id string) bool { return idRe.MatchString(id) }

type Store struct {
	dir string
	mu  sync.Mutex
	// OnFinish, if set, is called after a job is marked done or failed, in
	// whichever process finished it.
	OnFinish func(*Job)
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) jsonPath(id string) string { return filepath.Join(s.dir, id+".json") }

// LogPath is where a job's output goes.
func (s *Store) LogPath(id string) string { return filepath.Join(s.dir, id+".log") }

func newID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Create records a new running job and trims old ones.
func (s *Store) Create(j Job) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.ID = newID()
	j.State = Running
	j.Started = time.Now().UTC()
	if err := s.write(&j); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.LogPath(j.ID), nil, 0o644); err != nil {
		return nil, err
	}
	s.prune()
	return &j, nil
}

// Save writes a job's current state.
func (s *Store) Save(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(j)
}

// Finish marks a job done or failed.
func (s *Store) Finish(j *Job, res *Result, err error) error {
	now := time.Now().UTC()
	j.Finished, j.Result, j.State = &now, res, Done
	if err != nil {
		j.State, j.Error = Failed, err.Error()
	}
	if err := s.Save(j); err != nil {
		return err
	}
	if s.OnFinish != nil {
		s.OnFinish(j)
	}
	return nil
}

func (s *Store) write(j *Job) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.jsonPath(j.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.jsonPath(j.ID))
}

var ErrNotFound = errors.New("no such job")

func (s *Store) Get(id string) (*Job, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(s.jsonPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("job %s: %w", id, err)
	}
	return &j, nil
}

func (s *Store) ids() []string {
	entries, _ := os.ReadDir(s.dir)
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && ValidID(id) {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

// List returns the newest jobs first, for one stack or all ("").
func (s *Store) List(stack string, limit int) []*Job {
	out := []*Job{}
	for _, id := range s.ids() {
		j, err := s.Get(id)
		if err != nil || (stack != "" && j.Stack != stack) {
			continue
		}
		out = append(out, j)
		if len(out) == limit {
			break
		}
	}
	return out
}

// Active returns a stack's running job, if any.
func (s *Store) Active(stack string) *Job {
	for _, j := range s.List(stack, 20) {
		if j.State == Running {
			return j
		}
	}
	return nil
}

// Latest returns a stack's most recent finished job.
func (s *Store) Latest(stack string) *Job {
	for _, j := range s.List(stack, 20) {
		if j.State != Running {
			return j
		}
	}
	return nil
}

// Recover fails jobs left running by a Hoist that stopped mid-deploy. Self
// deploys run in a helper container and are left alone while helperAlive
// says it's still there.
func (s *Store) Recover(helperAlive func(*Job) bool) {
	for _, j := range s.List("", keep) {
		if j.State != Running {
			continue
		}
		if j.Self && helperAlive(j) {
			continue
		}
		_ = s.Finish(j, nil, errors.New("interrupted: Hoist stopped during the deploy"))
	}
}

// HelperName is the container that runs a self deploy.
func HelperName(id string) string { return "hoist-deploy-" + id }

func (s *Store) prune() {
	ids := s.ids()
	for _, id := range ids[min(keep, len(ids)):] {
		os.Remove(s.jsonPath(id))
		os.Remove(s.LogPath(id))
	}
}
