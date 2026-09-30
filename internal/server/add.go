package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/envfile"
	"github.com/audemed44/hoist/internal/gitrepo"
	"github.com/audemed44/hoist/internal/jobs"
)

// candidate is a running compose project Hoist doesn't manage yet, with the
// stack it would become.
type candidate struct {
	docker.ProjectInfo
	Stack string `json:"stack"` // suggested name
	Path  string `json:"path"`
	File  string `json:"file"`
	// Problem says why it can't be adopted as it is.
	Problem string `json:"problem,omitempty"`
}

// visible checks that Hoist sees path at the same path as the host does,
// which compose needs to resolve relative bind mounts. Outside a container
// every path is the host's.
func (s *Server) visible(path string) error {
	self := s.findSelf()
	if self == nil {
		return nil
	}
	var mounts [][2]string
	for _, b := range self.Binds {
		if parts := strings.Split(b, ":"); len(parts) >= 2 {
			mounts = append(mounts, [2]string{parts[0], parts[1]})
		}
	}
	for _, m := range self.Mounts {
		if m.Type == "bind" {
			mounts = append(mounts, [2]string{m.Source, m.Target})
		}
	}
	for _, m := range mounts {
		src, dst := filepath.Clean(m[0]), filepath.Clean(m[1])
		if src == dst && (path == dst || strings.HasPrefix(path, dst+"/")) {
			return nil
		}
	}
	return fmt.Errorf("Hoist can't see %s at the same path as the host; mount its folder into Hoist's container at the same path (as your stacks are)", path)
}

var nameClean = regexp.MustCompile(`[^a-z0-9_-]+`)

// suggestName turns a folder or project name into a free stack name.
func (s *Server) suggestName(from string) string {
	base := strings.Trim(nameClean.ReplaceAllString(strings.ToLower(from), "-"), "-_")
	if base == "" {
		base = "stack"
	}
	name := base
	for i := 2; ; i++ {
		if _, taken := s.Config.Stack(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

type discovery struct {
	// Projects are the compose projects with containers on this host that no
	// stack covers yet.
	Projects []candidate `json:"projects"`
	// Parents are the folders the stacks live in, where a new one would go.
	Parents []string `json:"parents"`
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Docker.Projects(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	managed := map[string]bool{}
	resp := discovery{Projects: []candidate{}, Parents: []string{}}
	for _, st := range s.Config.List() {
		managed[st.Project] = true
		if parent := filepath.Dir(st.Path); !slices.Contains(resp.Parents, parent) {
			resp.Parents = append(resp.Parents, parent)
		}
	}
	for _, p := range projects {
		if managed[p.Name] {
			continue
		}
		c := candidate{ProjectInfo: p, Path: p.Dir}
		if p.Dir != "" {
			c.Stack = s.suggestName(filepath.Base(p.Dir))
		} else {
			c.Stack = s.suggestName(p.Name)
		}
		switch {
		case p.Dir == "" || len(p.Files) == 0:
			c.Problem = "compose didn't record where this project was started from"
		case len(p.Files) > 1:
			c.Problem = fmt.Sprintf("started from %d compose files; Hoist handles one file per stack", len(p.Files))
		case filepath.Dir(p.Files[0]) != filepath.Clean(p.Dir):
			c.Problem = "its compose file isn't in the project folder"
		default:
			c.File = filepath.Base(p.Files[0])
			if err := s.visible(p.Dir); err != nil {
				c.Problem = err.Error()
			} else if _, err := os.Stat(p.Files[0]); err != nil {
				c.Problem = "Hoist can't read " + p.Files[0]
			}
		}
		resp.Projects = append(resp.Projects, c)
	}
	writeJSON(w, http.StatusOK, resp)
}

type addRequest struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Project string `json:"project"`
	File    string `json:"file"`
	// Create makes the folder and compose file (Content); otherwise the
	// folder and file must exist (adopting).
	Create  bool   `json:"create"`
	Content string `json:"content"`
	Message string `json:"message"`
	Deploy  bool   `json:"deploy"`
}

type addResult struct {
	Stack     string    `json:"stack"`
	Commit    string    `json:"commit,omitempty"`
	Pushed    bool      `json:"pushed"`
	PushError string    `json:"push_error,omitempty"`
	GitNote   string    `json:"git_note,omitempty"`
	Job       *jobs.Job `json:"job,omitempty"`
}

// addStack adopts an existing stack folder or creates a new one, and adds it
// to hoist.yaml.
func (s *Server) addStack(w http.ResponseWriter, r *http.Request) {
	var body addRequest
	if !readJSON(w, r, maxCompose+(16<<10), &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Path = strings.TrimSpace(body.Path)
	body.Project = strings.TrimSpace(body.Project)
	body.File = strings.TrimSpace(body.File)
	switch {
	case !config.ValidName(body.Name):
		writeError(w, http.StatusUnprocessableEntity, "the name must be lowercase letters, digits, - and _")
		return
	case !filepath.IsAbs(body.Path):
		writeError(w, http.StatusUnprocessableEntity, "the folder must be an absolute path, as on the host")
		return
	}
	body.Path = filepath.Clean(body.Path)
	if _, taken := s.Config.Stack(body.Name); taken {
		writeError(w, http.StatusConflict, "there is already a stack named "+body.Name)
		return
	}
	if err := s.visible(body.Path); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if body.Create {
		s.createStack(w, r, body)
		return
	}
	info, err := os.Stat(body.Path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusUnprocessableEntity, body.Path+" isn't a folder Hoist can see")
		return
	}
	st, err := s.Config.AddStack(config.Stack{Name: body.Name, Path: body.Path, File: body.File, Project: body.Project})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.record(audit.Event{
		Stack: st.Name, Action: audit.StackAdopt, Trigger: triggerOf(r),
		Detail: fmt.Sprintf("project %s from %s", st.Project, st.ComposePath()),
	})
	writeJSON(w, http.StatusCreated, addResult{Stack: st.Name})
}

func (s *Server) createStack(w http.ResponseWriter, r *http.Request, body addRequest) {
	if body.File == "" {
		body.File = "compose.yml"
	}
	if body.Project == "" {
		body.Project = config.ProjectName(body.Path)
	}
	st := config.Stack{Name: body.Name, Path: body.Path, File: body.File, Project: body.Project}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = "feat(" + st.Name + "): add stack"
	}
	if !s.messageOK(w, message) {
		return
	}
	if parent, err := os.Stat(filepath.Dir(st.Path)); err != nil || !parent.IsDir() {
		writeError(w, http.StatusUnprocessableEntity, filepath.Dir(st.Path)+" doesn't exist")
		return
	}
	if strings.Contains(st.File, "/") || strings.HasPrefix(st.File, ".") {
		writeError(w, http.StatusUnprocessableEntity, "the file must be a file name inside the folder")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if projects, err := s.Docker.Projects(ctx); err == nil {
		for _, p := range projects {
			if p.Name == st.Project {
				writeError(w, http.StatusConflict, fmt.Sprintf(
					"a compose project named %s already runs from %s; adopt it instead, or pick another project name", p.Name, p.Dir))
				return
			}
		}
	}

	madeDir := false
	switch info, err := os.Stat(st.Path); {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(st.Path, 0o755); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		madeDir = true
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	case !info.IsDir():
		writeError(w, http.StatusConflict, st.Path+" is a file")
		return
	default:
		_, statErr := os.Stat(st.ComposePath())
		if config.DefaultFile(st.Path) != "" || statErr == nil {
			writeError(w, http.StatusConflict, st.Path+" already has a compose file; adopt it instead")
			return
		}
	}
	undo := func() {
		os.Remove(st.ComposePath())
		if madeDir {
			os.Remove(st.Path)
		}
	}
	content := compose.ToLF([]byte(body.Content))
	if err := compose.Validate(ctx, st, content); err != nil {
		undo()
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := envfile.WriteAtomic(st.ComposePath(), content, 0o644); err != nil {
		undo()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, err := s.Config.AddStack(st)
	if err != nil {
		undo()
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	res := addResult{Stack: st.Name}
	ev := audit.Event{Stack: st.Name, Action: audit.StackCreate, Trigger: triggerOf(r), Detail: message}
	if err := s.commitNew(ctx, st, message, &res); err != nil {
		ev.Error = "created, but not committed: " + err.Error()
		res.GitNote = ev.Error
	}
	ev.Commit = res.Commit
	if res.PushError != "" && ev.Error == "" {
		ev.Error = "not pushed: " + res.PushError
	}
	s.record(ev)
	if body.Deploy {
		job, _, err := s.startDeploy(st, nil, triggerOf(r), res.Commit)
		if err != nil {
			res.GitNote = strings.TrimSpace(res.GitNote + " The deploy didn't start: " + err.Error())
		}
		res.Job = job
	}
	writeJSON(w, http.StatusCreated, res)
}

// commitNew commits a new stack's compose file, when its folder is in a git
// repo. A .gitignore allowlist would leave the file out, so it's added
// explicitly, and the note says so.
func (s *Server) commitNew(ctx context.Context, st config.Stack, message string, res *addResult) error {
	repo, err := gitrepo.Open(ctx, st.Path)
	if err != nil || repo == nil {
		return err
	}
	ignored, err := repo.Ignored(ctx, st.ComposePath())
	if err != nil {
		return err
	}
	if ignored {
		if err := repo.Track(ctx, st.ComposePath()); err != nil {
			return err
		}
		rel, _ := repo.Rel(st.ComposePath())
		res.GitNote = rel + " is excluded by .gitignore, so Hoist added it anyway (git add --force). Add it to .gitignore's allowlist to keep things tidy."
	}
	var save saveResult
	if err := s.commit(ctx, st, message, &save); err != nil {
		return err
	}
	res.Commit, res.Pushed, res.PushError = save.Commit, save.Pushed, save.PushError
	return nil
}
