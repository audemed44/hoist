package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/github"
	"github.com/audemed44/hoist/internal/registry"
)

func TestRepoOf(t *testing.T) {
	for src, want := range map[string]string{
		"https://github.com/audemed44/foyer":      "audemed44/foyer",
		"https://github.com/audemed44/foyer.git":  "audemed44/foyer",
		"https://github.com/audemed44/foyer/":     "audemed44/foyer",
		"https://gitlab.com/audemed44/foyer":      "",
		"https://github.com/audemed44":            "",
		"https://github.com/audemed44/foyer/tree": "",
	} {
		if got := RepoOf(src); got != want {
			t.Errorf("RepoOf(%s) = %q", src, got)
		}
	}
}

func run(sha, status, conclusion string) *github.Run {
	return &github.Run{SHA: sha, Status: status, Conclusion: conclusion}
}

func TestAppState(t *testing.T) {
	behind := App{Head: "h2", Running: Running{Revision: "h1", Digest: "sha256:a"}}
	for _, c := range []struct {
		name string
		app  App
		want string
	}{
		{"up to date", App{Head: "h1", Running: Running{Revision: "h1", Digest: "d"}, Latest: "d"}, Deployed},
		{"building", with(behind, run("h2", "in_progress", ""), ""), Building},
		{"build failed", with(behind, run("h2", "completed", "failure"), ""), BuildFailed},
		{"built", with(behind, run("h2", "completed", "success"), "sha256:a"), Ready},
		{"older build, new digest", with(behind, run("h0", "completed", "success"), "sha256:b"), Ready},
		{"no build for the new commits", with(behind, run("h0", "completed", "success"), "sha256:a"), NotBuilt},
		{"rebuilt the same commit", App{Head: "h1", Running: Running{Revision: "h1", Digest: "a"}, Latest: "b"}, Ready},
		{"no revision label", App{Head: "h1", Running: Running{Digest: "a"}, Latest: "a"}, Deployed},
	} {
		if got := AppState(c.app); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func with(a App, b *github.Run, latest string) App {
	a.Build, a.Latest = b, latest
	return a
}

func TestPRState(t *testing.T) {
	yes, no := true, false
	ok := PR{PullRequest: github.PullRequest{Mergeable: &yes, Rebaseable: &yes, MergeableState: "clean"}, CI: CIPassing}
	for _, c := range []struct {
		name string
		edit func(*PR)
		want string
	}{
		{"ready", func(*PR) {}, PRReady},
		{"no checks", func(p *PR) { p.CI = CINone }, PRReady},
		{"draft", func(p *PR) { p.Draft = true }, PRDraft},
		{"can't rebase", func(p *PR) { p.Rebaseable = &no }, PRConflict},
		{"failing", func(p *PR) { p.CI = CIFailing }, PRFailing},
		{"running", func(p *PR) { p.CI = CIPending }, PRRunning},
		{"unknown", func(p *PR) { p.Mergeable = nil }, PRChecking},
		{"blocked", func(p *PR) { p.MergeableState = "blocked" }, PRBlocked},
		{"changes", func(p *PR) { p.Review = "changes_requested" }, PRChanges},
	} {
		p := ok
		c.edit(&p)
		if got := PRState(p); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestCIAndLatest(t *testing.T) {
	t0 := time.Now()
	runs := []github.Run{
		{ID: 1, Workflow: 1, Name: "test", Status: "completed", Conclusion: "failure", Created: t0},
		{ID: 2, Workflow: 1, Name: "test", Status: "completed", Conclusion: "success", Created: t0.Add(time.Minute)},
		{ID: 3, Workflow: 2, Name: "lint", Status: "completed", Conclusion: "skipped", Created: t0},
	}
	latest := LatestPerWorkflow(runs)
	if len(latest) != 2 || latest[1].ID != 2 || CI(latest) != CIPassing {
		t.Fatalf("latest = %+v, ci %s", latest, CI(latest))
	}
	if CI(nil) != CINone || CI(runs[:1]) != CIFailing {
		t.Error("ci")
	}
	if CI([]github.Run{{Status: "queued"}}) != CIPending {
		t.Error("pending")
	}
}

// callLog records the requests a fake server got.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) add(s string) {
	c.mu.Lock()
	c.calls = append(c.calls, s)
	c.mu.Unlock()
}

func (c *callLog) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// fakeGitHub answers like api.github.com for one repository, me/app.
func fakeGitHub(t *testing.T, calls *callLog) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls.add(r.Method + " " + r.URL.Path)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		run := func(id, wf int, sha, status, conclusion string, created string) string {
			return fmt.Sprintf(`{"id":%d,"name":"wf%d","workflow_id":%d,"path":".github/workflows/x.yml","head_sha":%q,"status":%q,"conclusion":%q,"created_at":%q,"html_url":"https://github.com/me/app/actions/runs/%d"}`,
				id, wf, wf, sha, status, conclusion, created, id)
		}
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"login":"me"}`)
		case "/repos/me/app":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case "/repos/me/app/branches/main":
			fmt.Fprint(w, `{"commit":{"sha":"h2"}}`)
		case "/repos/me/app/compare/r1...h2":
			fmt.Fprint(w, `{"status":"ahead","ahead_by":2,"behind_by":0,"html_url":"https://github.com/me/app/compare/r1...h2","commits":[
				{"sha":"c1","commit":{"message":"feat: a","author":{"name":"me","date":"2026-10-01T10:00:00Z"}}},
				{"sha":"h2","commit":{"message":"fix: b\n\nmore","author":{"name":"me","date":"2026-10-01T11:00:00Z"}}}]}`)
		case "/repos/me/app/actions/workflows/docker.yml/runs":
			fmt.Fprint(w, `{"workflow_runs":[`+run(9, 2, "h2", "completed", "success", "2026-10-01T11:01:00Z")+`]}`)
		case "/repos/me/app/pulls":
			fmt.Fprint(w, `[{"number":5,"title":"feat: c","head":{"ref":"feat/c","sha":"p5","repo":{"full_name":"me/app"}},"base":{"repo":{"full_name":"me/app"}},"user":{"login":"me"}}]`)
		case "/repos/me/app/pulls/5":
			fmt.Fprint(w, `{"number":5,"title":"feat: c","head":{"ref":"feat/c","sha":"p5","repo":{"full_name":"me/app"}},"base":{"repo":{"full_name":"me/app"}},"user":{"login":"me"},
				"mergeable":true,"rebaseable":true,"mergeable_state":"clean"}`)
		case "/repos/me/app/actions/runs":
			if r.URL.Query().Get("head_sha") != "p5" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"workflow_runs":[`+run(7, 1, "p5", "completed", "success", "2026-10-01T12:05:00Z")+`,`+run(6, 1, "p5", "completed", "failure", "2026-10-01T12:00:00Z")+`]}`)
		case "/repos/me/app/pulls/5/reviews":
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func fakeDocker(t *testing.T) *docker.Client {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			ctr := func(name, project, image, source, rev string) map[string]any {
				return map[string]any{"Id": name, "Names": []string{"/" + name}, "Image": image, "ImageID": "sha256:" + name,
					"State": "running", "Created": 1700000000, "Labels": map[string]string{
						"com.docker.compose.project": project, "com.docker.compose.service": name,
						"org.opencontainers.image.source": source, "org.opencontainers.image.revision": rev}}
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				ctr("app", "main-server", "ghcr.io/me/app:latest", "https://github.com/me/app", "r1"),
				ctr("theirs", "main-server", "ghcr.io/other/x:latest", "https://github.com/other/x", "z"),
				ctr("elsewhere", "not-a-stack", "ghcr.io/me/y:latest", "https://github.com/me/y", "y"),
			})
		case "/images/sha256:app/json":
			fmt.Fprint(w, `{"Id":"sha256:app","RepoDigests":["ghcr.io/me/app@sha256:running"]}`)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return docker.New(sock)
}

type redirect struct {
	host string
	rt   http.RoundTripper
}

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Host = r.host
	return r.rt.RoundTrip(req)
}

func fakeRegistry(t *testing.T) *registry.Client {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/me/app/manifests/latest" {
			w.Header().Set("Docker-Content-Digest", "sha256:published")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return registry.NewWith(&http.Client{Transport: redirect{strings.TrimPrefix(srv.URL, "https://"), srv.Client().Transport}})
}

func testConfig(t *testing.T) *config.Config {
	dir := t.TempDir()
	stack := filepath.Join(dir, "main-stack")
	_ = os.MkdirAll(stack, 0o755)
	_ = os.WriteFile(filepath.Join(stack, "compose.yml"), []byte("services: {}\n"), 0o644)
	path := filepath.Join(dir, "hoist.yaml")
	_ = os.WriteFile(path, []byte("stacks:\n  - name: main-stack\n    path: "+stack+"\n    project: main-server\n"), 0o644)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBoard(t *testing.T) {
	var calls callLog
	gh := fakeGitHub(t, &calls)
	defer gh.Close()
	src := &Source{
		GitHub: github.NewWith(gh.URL, "tok", gh.Client()), Registry: fakeRegistry(t),
		Docker: fakeDocker(t), Config: testConfig(t),
	}
	b := src.Get(context.Background(), time.Minute)
	if b.Error != "" || len(b.Owners) != 1 || b.Owners[0] != "me" || len(b.Apps) != 1 {
		t.Fatalf("board = %+v", b)
	}
	a := b.Apps[0]
	if a.Repo != "me/app" || a.Stack != "main-stack" || a.Branch != "main" || a.Head != "h2" || len(a.Errors) > 0 {
		t.Fatalf("app = %+v", a)
	}
	if a.Running.Revision != "r1" || a.Running.Digest != "sha256:running" || a.Latest != "sha256:published" {
		t.Errorf("versions = %+v, latest %s", a.Running, a.Latest)
	}
	if a.Behind != 2 || len(a.Commits) != 2 || a.Commits[0].Message != "fix: b" || a.Build == nil || a.Build.ID != 9 {
		t.Errorf("behind %d, commits %+v, build %+v", a.Behind, a.Commits, a.Build)
	}
	if a.State != Ready {
		t.Errorf("state = %s", a.State)
	}
	if len(a.PRs) != 1 {
		t.Fatalf("prs = %+v", a.PRs)
	}
	p := a.PRs[0]
	if p.Number != 5 || !p.SameRepo || p.CI != CIPassing || p.State != PRReady || len(p.Runs) != 1 || p.Runs[0].ID != 7 {
		t.Errorf("pr = %+v", p)
	}
	if s := b.Summary(); s != (Summary{PRs: 1, Ready: 1, ToMerge: 1}) {
		t.Errorf("summary = %+v", s)
	}

	// Cached for maxAge; after that GitHub is asked again, conditionally.
	n := len(calls.list())
	src.Get(context.Background(), time.Minute)
	if len(calls.list()) != n {
		t.Error("the board wasn't cached")
	}
	again := src.Get(context.Background(), 0)
	if again.Apps[0].State != Ready || again.Apps[0].PRs[0].State != PRReady {
		t.Errorf("from ETags: %+v", again.Apps[0])
	}
	for _, c := range calls.list()[n:] {
		if c == "GET /user" || c == "GET /repos/me/app" {
			t.Errorf("asked again for %s", c)
		}
	}
}

func TestNoToken(t *testing.T) {
	src := &Source{Docker: fakeDocker(t), Config: testConfig(t)}
	if b := src.Get(context.Background(), time.Minute); b.Configured || len(b.Apps) != 0 {
		t.Errorf("board = %+v", b)
	}
}
