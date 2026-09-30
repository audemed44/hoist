package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/envfile"
	"github.com/audemed44/hoist/internal/gitrepo"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/updates"
)

// fetchAge is how stale the remote-tracking branch may get before a page
// load fetches again.
const fetchAge = 2 * time.Minute

// cachedServices keeps a resolved compose file until it or .env changes;
// resolving runs compose twice, which takes a moment.
type cachedServices struct {
	key      string
	services []compose.Service
	err      error
}

func fileKey(paths ...string) string {
	key := ""
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			key += p + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(info.Size(), 10) + ";"
		}
	}
	return key
}

func (s *Server) services(ctx context.Context, st config.Stack) ([]compose.Service, error) {
	key := fileKey(st.ComposePath(), st.EnvPath())
	s.mu.Lock()
	c, ok := s.plans[st.Name]
	s.mu.Unlock()
	if ok && c.key == key {
		return c.services, c.err
	}
	svcs, err := compose.Services(ctx, st)
	s.mu.Lock()
	s.plans[st.Name] = cachedServices{key: key, services: svcs, err: err}
	s.mu.Unlock()
	return svcs, err
}

type Counts struct {
	Services int `json:"services"`
	Running  int `json:"running"`
	Pending  int `json:"pending"` // services a deploy would change
	Updates  int `json:"updates"` // services with a newer image or version
}

type StackInfo struct {
	Name     string                 `json:"name"`
	Path     string                 `json:"path"`
	File     string                 `json:"file"`
	Project  string                 `json:"project"`
	Self     bool                   `json:"self"`
	Counts   Counts                 `json:"counts"`
	Services []compose.ServiceState `json:"services"`
	// Error is why the compose file couldn't be resolved.
	Error    string          `json:"error,omitempty"`
	Git      *gitrepo.Status `json:"git,omitempty"`
	GitError string          `json:"git_error,omitempty"`
	Active   *jobs.Job       `json:"active,omitempty"`
	Last     *jobs.Job       `json:"last,omitempty"`
	// Updates are the services the last update check found updates for.
	Updates []updates.Service `json:"updates"`
}

func (s *Server) stackInfo(ctx context.Context, st config.Stack, fetch bool) StackInfo {
	info := StackInfo{Name: st.Name, Path: st.Path, File: st.File, Project: st.Project, Self: s.isSelf(st)}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		repo, err := gitrepo.Open(ctx, st.Path)
		if err != nil {
			info.GitError = err.Error()
			return
		}
		if repo == nil {
			return
		}
		age := time.Duration(0)
		if fetch {
			age = fetchAge
		}
		status, err := repo.Status(ctx, st.ComposePath(), age)
		if err != nil {
			info.GitError = err.Error()
			return
		}
		info.Git = &status
	}()
	svcs, err := s.services(ctx, st)
	containers, cerr := s.Docker.Project(ctx, st.Project)
	switch {
	case err != nil:
		info.Error = err.Error()
	case cerr != nil:
		info.Error = "docker: " + cerr.Error()
	default:
		info.Services = compose.Plan(svcs, containers, st.ShouldRemoveOrphans())
	}
	if info.Services == nil {
		info.Services = []compose.ServiceState{}
	}
	for _, svc := range info.Services {
		if svc.Change != compose.Unchanged {
			info.Counts.Pending++
		}
		if svc.Orphan {
			continue
		}
		info.Counts.Services++
		if svc.Container != nil && svc.Container.State == "running" {
			info.Counts.Running++
		}
	}
	if s.Updates != nil {
		state, _ := s.Updates.State()
		info.Updates = state.Sorted(st.Name)
	}
	if info.Updates == nil {
		info.Updates = []updates.Service{}
	}
	info.Counts.Updates = len(info.Updates)
	info.Active = s.active(st.Name)
	info.Last = s.Jobs.Latest(st.Name)
	wg.Wait()
	return info
}

func (s *Server) listStacks(w http.ResponseWriter, r *http.Request) {
	stacks := s.Config.List()
	out := make([]StackInfo, len(stacks))
	var wg sync.WaitGroup
	for i, st := range stacks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = s.stackInfo(r.Context(), st, true)
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// stackNames lists the configured stacks without looking at any of them.
func (s *Server) stackNames(w http.ResponseWriter, _ *http.Request) {
	names := []string{}
	for _, st := range s.Config.List() {
		names = append(names, st.Name)
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) getStack(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.stackInfo(r.Context(), st, true))
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// composeFile is a compose file as the editor sees it: always with \n line
// endings. Hash is of the file as it is on disk; CRLF says it's stored with
// \r\n, which the next save turns into \n.
type composeFile struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
	CRLF    bool   `json:"crlf,omitempty"`
}

func newComposeFile(data []byte) composeFile {
	return composeFile{Content: string(compose.ToLF(data)), Hash: contentHash(data), CRLF: compose.UsesCRLF(data)}
}

func (s *Server) getCompose(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	data, err := os.ReadFile(st.ComposePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, newComposeFile(data))
}

const maxCompose = 1 << 20

// checkCompose validates an edit and suggests a commit message for it.
func (s *Server) checkCompose(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !readJSON(w, r, maxCompose, &body) {
		return
	}
	current, _ := os.ReadFile(st.ComposePath())
	resp := map[string]string{"message": compose.Summary(st.Name, current, []byte(body.Content))}
	if err := compose.Validate(r.Context(), st, []byte(body.Content)); err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

type saveResult struct {
	Hash      string    `json:"hash"`
	Commit    string    `json:"commit,omitempty"`
	Pushed    bool      `json:"pushed"`
	PushError string    `json:"push_error,omitempty"`
	Job       *jobs.Job `json:"job,omitempty"`
}

func (s *Server) putCompose(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
		// Base is the hash of the file the edit started from.
		Base    string `json:"base"`
		Message string `json:"message"`
		Deploy  bool   `json:"deploy"`
	}
	if !readJSON(w, r, maxCompose, &body) {
		return
	}
	current, err := os.ReadFile(st.ComposePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Base != "" && body.Base != contentHash(current) {
		writeError(w, http.StatusConflict, "the compose file changed since you opened it; reload to see the new version")
		return
	}
	content := compose.ToLF([]byte(body.Content))
	if err := compose.Validate(r.Context(), st, content); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = compose.Summary(st.Name, current, content)
	}
	if !s.messageOK(w, message) {
		return
	}
	if err := envfile.WriteAtomic(st.ComposePath(), content, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res := saveResult{Hash: contentHash(content)}
	ev := audit.Event{Stack: st.Name, Action: audit.ComposeSave, Trigger: triggerOf(r), Detail: message}
	if err := s.commit(r.Context(), st, message, &res); err != nil {
		ev.Result, ev.Error = audit.Failed, "saved, but the commit failed: "+err.Error()
		s.record(ev)
		writeError(w, http.StatusInternalServerError, ev.Error)
		return
	}
	ev.Commit = res.Commit
	if res.PushError != "" {
		ev.Error = "not pushed: " + res.PushError
	}
	s.record(ev)
	if body.Deploy {
		job, status, err := s.startDeploy(st, nil, triggerOf(r), res.Commit)
		if err != nil {
			writeError(w, status, "saved, but the deploy didn't start: "+err.Error())
			return
		}
		res.Job = job
	}
	writeJSON(w, http.StatusOK, res)
}

// messageOK refuses a commit message that isn't a Conventional Commit, when
// the config asks for them.
func (s *Server) messageOK(w http.ResponseWriter, message string) bool {
	if s.Config.Git.EnforceConventional() && !compose.Conventional(message) {
		writeError(w, http.StatusUnprocessableEntity,
			`commit messages must follow Conventional Commits, e.g. "chore(main-stack): bump shelfloom 0.4 → 0.5"`)
		return false
	}
	return true
}

// commit records the compose file in git (and pushes) when the stack lives
// in a repo; otherwise it does nothing.
func (s *Server) commit(ctx context.Context, st config.Stack, message string, res *saveResult) error {
	repo, err := gitrepo.Open(ctx, st.Path)
	if err != nil || repo == nil {
		return err
	}
	author := gitrepo.Author{Name: s.Config.Git.Name, Email: s.Config.Git.Email}
	hash, err := repo.Commit(ctx, st.ComposePath(), message, author)
	if errors.Is(err, gitrepo.ErrNothingToCommit) {
		return nil
	}
	if err != nil {
		return err
	}
	res.Commit = hash
	if s.Config.Git.ShouldPush() {
		if err := repo.Push(ctx); err != nil {
			res.PushError = err.Error()
		} else {
			res.Pushed = true
		}
	}
	return nil
}

type envEntry struct {
	Key string `json:"key"`
	Set bool   `json:"set"` // has a non-empty value
}

type envResponse struct {
	Exists  bool       `json:"exists"`
	Entries []envEntry `json:"entries"`
	// Missing are used by the compose file without a default, but not set.
	Missing []string `json:"missing"`
	// Unused aren't referenced by the compose file (they may still be read
	// through env_file).
	Unused []string `json:"unused"`
}

// getEnv lists the .env keys. Values stay on the server until asked for one
// at a time.
func (s *Server) getEnv(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	f, err := envfile.Read(st.EnvPath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, statErr := os.Stat(st.EnvPath())
	resp := envResponse{Exists: statErr == nil, Entries: []envEntry{}, Missing: []string{}, Unused: []string{}}
	seen := map[string]bool{}
	for _, k := range f.Keys() {
		if seen[k] {
			continue
		}
		seen[k] = true
		v, _ := f.Get(k)
		resp.Entries = append(resp.Entries, envEntry{Key: k, Set: v != "" && v != `""` && v != "''"})
	}
	if data, err := os.ReadFile(st.ComposePath()); err == nil {
		all, required := envfile.Refs(data)
		for _, k := range required {
			if !seen[k] {
				resp.Missing = append(resp.Missing, k)
			}
		}
		for _, e := range resp.Entries {
			if !slices.Contains(all, e.Key) {
				resp.Unused = append(resp.Unused, e.Key)
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getEnvValue(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	f, err := envfile.Read(st.EnvPath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, ok := f.Get(r.PathValue("key"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such variable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *Server) putEnv(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	var body struct {
		Entries []envfile.Change `json:"entries"`
	}
	if !readJSON(w, r, 256<<10, &body) {
		return
	}
	f, err := envfile.Read(st.EnvPath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	before := envfile.Parse(f.Bytes())
	if err := f.Apply(body.Entries); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ev := audit.Event{Stack: st.Name, Action: audit.EnvSave, Trigger: triggerOf(r), Detail: envChanges(before, f)}
	if err := f.Write(st.EnvPath()); err != nil {
		ev.Result, ev.Error = audit.Failed, err.Error()
		s.record(ev)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.record(ev)
	s.getEnv(w, r)
}

func (s *Server) repo(w http.ResponseWriter, r *http.Request, st config.Stack) *gitrepo.Repo {
	repo, err := gitrepo.Open(r.Context(), st.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if repo == nil {
		writeError(w, http.StatusNotFound, "this stack isn't in a git repo")
	}
	return repo
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	repo := s.repo(w, r, st)
	if repo == nil {
		return
	}
	commits, err := repo.Log(r.Context(), st.ComposePath(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, commits)
}

func (s *Server) getHistoryFile(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	repo := s.repo(w, r, st)
	if repo == nil {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path, _ = repo.Rel(st.ComposePath())
	}
	data, err := repo.Show(r.Context(), r.PathValue("hash"), path)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, newComposeFile(data))
}

func (s *Server) gitFetch(w http.ResponseWriter, r *http.Request) {
	s.gitAction(w, r, "", "", func(ctx context.Context, repo *gitrepo.Repo, _ config.Stack) error { return repo.Fetch(ctx) })
}

func (s *Server) gitPull(w http.ResponseWriter, r *http.Request) {
	s.gitAction(w, r, audit.GitPull, "", func(ctx context.Context, repo *gitrepo.Repo, _ config.Stack) error { return repo.Pull(ctx) })
}

func (s *Server) gitPush(w http.ResponseWriter, r *http.Request) {
	s.gitAction(w, r, audit.GitPush, "", func(ctx context.Context, repo *gitrepo.Repo, _ config.Stack) error { return repo.Push(ctx) })
}

// driftResponse is a compose file edited outside Hoist next to its last
// commit, with a message for committing the difference.
type driftResponse struct {
	Committed composeFile `json:"committed"`
	Current   composeFile `json:"current"`
	Message   string      `json:"message"`
}

func (s *Server) getDrift(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	repo := s.repo(w, r, st)
	if repo == nil {
		return
	}
	head, err := repo.Committed(r.Context(), st.ComposePath())
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	current, err := os.ReadFile(st.ComposePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, driftResponse{
		Committed: newComposeFile(head), Current: newComposeFile(current),
		Message: compose.Summary(st.Name, head, current),
	})
}

// gitDiscard throws away edits made outside Hoist.
func (s *Server) gitDiscard(w http.ResponseWriter, r *http.Request) {
	s.gitAction(w, r, audit.GitDiscard, "discarded edits made outside Hoist", func(ctx context.Context, repo *gitrepo.Repo, st config.Stack) error {
		return repo.Restore(ctx, st.ComposePath())
	})
}

// gitCommit commits a compose file edited outside Hoist.
func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if !readJSON(w, r, 4<<10, &body) {
		return
	}
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		msg = "chore(" + st.Name + "): update compose file"
	}
	if !s.messageOK(w, msg) {
		return
	}
	s.gitAction(w, r, audit.GitCommit, msg, func(ctx context.Context, repo *gitrepo.Repo, st config.Stack) error {
		author := gitrepo.Author{Name: s.Config.Git.Name, Email: s.Config.Git.Email}
		if _, err := repo.Commit(ctx, st.ComposePath(), msg, author); err != nil {
			return err
		}
		if s.Config.Git.ShouldPush() {
			return repo.Push(ctx)
		}
		return nil
	})
}

// gitAction runs fn on the stack's repo and answers with the repo's status.
// A non-empty action is recorded in the audit log.
func (s *Server) gitAction(w http.ResponseWriter, r *http.Request, action, detail string, fn func(context.Context, *gitrepo.Repo, config.Stack) error) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	repo := s.repo(w, r, st)
	if repo == nil {
		return
	}
	err := fn(r.Context(), repo, st)
	status, serr := repo.Status(r.Context(), st.ComposePath(), 0)
	if action != "" {
		ev := audit.Event{Stack: st.Name, Action: action, Trigger: triggerOf(r), Detail: detail}
		if err != nil {
			ev.Result, ev.Error = audit.Failed, err.Error()
		} else if serr == nil {
			ev.Commit = status.Head
		}
		s.record(ev)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	err = serr
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
