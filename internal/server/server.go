// Package server exposes Hoist's JSON API and serves the built frontend.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/registry"
	"github.com/audemed44/hoist/internal/releases"
	"github.com/audemed44/hoist/internal/sleeping"
	"github.com/audemed44/hoist/internal/updates"
)

type Options struct {
	Config  *config.Config
	Docker  *docker.Client
	Jobs    *jobs.Store
	Audit   *audit.Log
	Updates *updates.Checker
	// Sleep knows which containers Gatehouse has put to sleep; nil without
	// Gatehouse.
	Sleep *sleeping.Client
	// Releases reads the release board; its GitHub client is nil without
	// a token.
	Releases *releases.Source
	Token    string
	ReadOnly bool
	// FoyerURL links the header back to Foyer, the homelab's start page.
	FoyerURL string
	Web      fs.FS
}

type Server struct {
	Options
	session string // cookie value for a signed-in browser

	mu       sync.Mutex
	starting map[string]bool // stacks whose deploy is being started
	plans    map[string]cachedServices
	judged   map[string]judgement // when each stack's latest deploy was last looked at

	selfOnce sync.Once
	self     *docker.Self // nil when Hoist isn't in a container

	registry *registry.Client
	ships    shipyard
}

func New(o Options) *Server {
	if o.Releases == nil {
		o.Releases = &releases.Source{Docker: o.Docker, Config: o.Config, Registry: registry.New()}
	}
	return &Server{
		Options: o, session: sessionValue(o.Token),
		starting: map[string]bool{}, plans: map[string]cachedServices{}, judged: map[string]judgement{},
		registry: registry.New(), ships: shipyard{ships: map[string]*Ship{}},
	}
}

func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/stacks", s.listStacks)
	api.HandleFunc("GET /api/stack-names", s.stackNames)
	api.HandleFunc("POST /api/stacks", s.writable(s.addStack))
	api.HandleFunc("GET /api/discover", s.discover)
	api.HandleFunc("GET /api/stacks/{name}", s.getStack)
	api.HandleFunc("GET /api/stacks/{name}/compose", s.getCompose)
	api.HandleFunc("POST /api/stacks/{name}/check", s.checkCompose)
	api.HandleFunc("POST /api/stacks/{name}/suggest-service", s.suggestService)
	api.HandleFunc("PUT /api/stacks/{name}/compose", s.writable(s.putCompose))
	api.HandleFunc("GET /api/stacks/{name}/env", s.getEnv)
	api.HandleFunc("GET /api/stacks/{name}/env/{key}", s.getEnvValue)
	api.HandleFunc("PUT /api/stacks/{name}/env", s.writable(s.putEnv))
	api.HandleFunc("GET /api/stacks/{name}/history", s.getHistory)
	api.HandleFunc("GET /api/stacks/{name}/history/{hash}", s.getHistoryFile)
	api.HandleFunc("POST /api/stacks/{name}/git/fetch", s.gitFetch)
	api.HandleFunc("POST /api/stacks/{name}/git/pull", s.writable(s.gitPull))
	api.HandleFunc("POST /api/stacks/{name}/git/push", s.writable(s.gitPush))
	api.HandleFunc("POST /api/stacks/{name}/git/commit", s.writable(s.gitCommit))
	api.HandleFunc("GET /api/stacks/{name}/drift", s.getDrift)
	api.HandleFunc("POST /api/stacks/{name}/git/discard", s.writable(s.gitDiscard))
	api.HandleFunc("POST /api/stacks/{name}/deploy", s.writable(s.postDeploy))
	api.HandleFunc("GET /api/stacks/{name}/deploys", s.listDeploys)
	api.HandleFunc("GET /api/stacks/{name}/rollback", s.getRollback)
	api.HandleFunc("POST /api/stacks/{name}/rollback", s.writable(s.postRollback))
	api.HandleFunc("POST /api/stacks/{name}/resume", s.writable(s.postResume))
	api.HandleFunc("GET /api/updates", s.getUpdates)
	api.HandleFunc("POST /api/updates/check", s.postCheck)
	api.HandleFunc("POST /api/stacks/{name}/services/{service}/update", s.writable(s.applyUpdate))
	api.HandleFunc("GET /api/releases", s.getReleases)
	api.HandleFunc("POST /api/releases/merge", s.writable(s.postMerge))
	api.HandleFunc("POST /api/releases/rerun", s.writable(s.postRerun))
	api.HandleFunc("POST /api/releases/deploy", s.writable(s.postReleaseDeploy))
	api.HandleFunc("POST /api/releases/ship", s.writable(s.postShip))
	api.HandleFunc("DELETE /api/releases/ships/{id}", s.writable(s.deleteShip))
	api.HandleFunc("GET /api/audit", s.listAudit)
	api.HandleFunc("GET /api/jobs", s.listJobs)
	api.HandleFunc("GET /api/jobs/{id}", s.getJob)
	api.HandleFunc("GET /api/jobs/{id}/log", s.jobLog)
	api.HandleFunc("GET /api/foyer/widget", s.foyerWidget)
	api.HandleFunc("POST /api/foyer/deploy/{name}", s.writable(s.foyerDeploy))
	api.HandleFunc("GET /api/foyer/jobs/{id}", s.foyerJob)
	api.HandleFunc("POST /api/foyer/releases/{owner}/{repo}/deploy", s.writable(s.foyerReleaseDeploy))
	api.HandleFunc("POST /api/foyer/releases/{owner}/{repo}/pulls/{number}/merge", s.writable(s.foyerReleaseMerge))
	api.HandleFunc("POST /api/foyer/releases/{owner}/{repo}/pulls/{number}/ship", s.writable(s.foyerReleaseShip))
	api.HandleFunc("GET /api/foyer/ships/{id}", s.foyerShip)
	api.HandleFunc("POST /api/foyer/ships/{id}/dismiss", s.writable(s.foyerDismissShip))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", s.getSession)
	mux.HandleFunc("POST /api/session", s.login)
	mux.HandleFunc("DELETE /api/session", s.logout)
	mux.Handle("/api/", s.requireAuth(api))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", s.spa())
	return securityHeaders(sameOrigin(mux))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes a JSON body of at most limit bytes.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "expected JSON")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}

// writable refuses changes in read-only mode.
func (s *Server) writable(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ReadOnly {
			writeError(w, http.StatusForbidden, "Hoist is in read-only mode (HOIST_READ_ONLY)")
			return
		}
		h(w, r)
	}
}

// stack resolves {name}, answering 404 itself when there's no such stack.
func (s *Server) stack(w http.ResponseWriter, r *http.Request) (config.Stack, bool) {
	st, ok := s.Config.Stack(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such stack")
	}
	return st, ok
}

// findSelf looks up Hoist's own container once.
func (s *Server) findSelf() *docker.Self {
	s.selfOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		self, err := s.Docker.FindSelf(ctx)
		if err != nil {
			if !errors.Is(err, docker.ErrNotFound) {
				slog.Warn("could not inspect Hoist's own container", "err", err)
			}
			return
		}
		s.self = self
	})
	return s.self
}

// isSelf reports whether st is the stack Hoist itself runs in.
func (s *Server) isSelf(st config.Stack) bool {
	self := s.findSelf()
	return self != nil && self.Project == st.Project
}

// spa serves the built frontend, falling back to index.html for app routes.
func (s *Server) spa() http.Handler {
	files := http.FileServer(http.FS(s.Web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if info, err := fs.Stat(s.Web, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.Web, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		// The editor injects its styles at runtime, hence 'unsafe-inline'.
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
