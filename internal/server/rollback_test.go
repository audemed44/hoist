package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/registry"
)

// fakeImages serves a stack running web (ghcr.io/a/web:latest, image
// sha256:new) and db (postgres:16), whose images and containers can be
// inspected, and records the tags Hoist adds.
type fakeImages struct {
	mu   sync.Mutex
	tags []string
}

func (f *fakeImages) install(e *env) {
	started := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	*e.docker = func(w http.ResponseWriter, r *http.Request) bool {
		p := r.URL.Path
		switch {
		case p == "/containers/json" && strings.Contains(r.URL.Query().Get("filters"), "project="):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "c-web", "Names": []string{"/web"}, "Image": "ghcr.io/a/web:latest", "ImageID": "sha256:new", "State": "running",
					"Labels": map[string]string{"com.docker.compose.service": "web", "com.docker.compose.config-hash": "h-web",
						"org.opencontainers.image.source": "https://github.com/a/web", "org.opencontainers.image.revision": "bbb"}},
				{"Id": "c-db", "Names": []string{"/db"}, "Image": "postgres:16", "ImageID": "sha256:pg", "State": "running",
					"Labels": map[string]string{"com.docker.compose.service": "db", "com.docker.compose.config-hash": "h-db"}},
			})
		case p == "/images/sha256:new/json":
			_, _ = w.Write([]byte(`{"Id":"sha256:new","RepoDigests":["ghcr.io/a/web@sha256:new-d"],"Config":{"Labels":{}}}`))
		case p == "/images/sha256:pg/json":
			_, _ = w.Write([]byte(`{"Id":"sha256:pg","RepoDigests":["postgres@sha256:pg-d"],"Config":{"Labels":{}}}`))
		case p == "/images/ghcr.io/a/web@sha256:old-d/json", p == "/images/postgres@sha256:pg-d/json":
			_, _ = w.Write([]byte(`{"Id":"sha256:x","RepoDigests":[],"Config":{"Labels":{}}}`))
		case p == "/containers/c-web/json", p == "/containers/c-db/json":
			_, _ = w.Write([]byte(`{"State":{"Running":true,"StartedAt":"` + started + `"}}`))
		case p == "/images/json":
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(p, "/tag") && r.Method == http.MethodPost:
			f.mu.Lock()
			f.tags = append(f.tags, r.URL.Query().Get("repo")+":"+r.URL.Query().Get("tag"))
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			return false
		}
		return true
	}
}

const (
	oldJob = "20261001-100000-aaaaaa"
	curJob = "20261002-100000-bbbbbb"
)

// withHistory records an older good deploy running web's previous image,
// and the current one, finished ten minutes ago.
func withHistory(t *testing.T, e *env) {
	t.Helper()
	ctx := t.Context()
	at := func(d time.Duration) *time.Time { x := time.Now().Add(-d).UTC(); return &x }
	db := jobs.Image{Service: "db", Container: "db", ContainerID: "c-db", Ref: "postgres:16", ImageID: "sha256:pg", Digest: "sha256:pg-d", State: "running"}
	old := &jobs.Job{ID: oldJob, Stack: "main-stack", Trigger: "ui", State: jobs.Done, Started: *at(3 * time.Hour), Finished: at(3 * time.Hour),
		Images: []jobs.Image{{Service: "web", Container: "web", ContainerID: "c-old", Ref: "ghcr.io/a/web:latest", ImageID: "sha256:old", Digest: "sha256:old-d", Revision: "aaa", State: "running"}, db}}
	cur := &jobs.Job{ID: curJob, Stack: "main-stack", Trigger: "ui", State: jobs.Done, Started: *at(10 * time.Minute), Finished: at(10 * time.Minute),
		Images: []jobs.Image{{Service: "web", Container: "web", ContainerID: "c-web", Ref: "ghcr.io/a/web:latest", ImageID: "sha256:new", Digest: "sha256:new-d", Revision: "bbb", State: "running"}, db}}
	for _, j := range []*jobs.Job{old, cur} {
		if err := e.srv.Audit.RecordDeploy(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.srv.Audit.MarkGood(ctx, oldJob, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestJudgeAndProtect(t *testing.T) {
	e := newEnv(t, false)
	var f fakeImages
	f.install(e)
	withHistory(t, e)
	list := decode[[]deployEntry](t, e.do("GET", "/api/stacks/main-stack/deploys", ""))
	if len(list) != 2 || !list[0].Current || list[0].Job != curJob {
		t.Fatalf("deploys = %+v", list)
	}
	if list[0].GoodAt == nil {
		t.Error("the current deploy ran healthy for long enough, but isn't marked good")
	}
	slices.Sort(f.tags)
	want := []string{
		"hoist-keep/main-stack:" + oldJob + "-db", "hoist-keep/main-stack:" + oldJob + "-web",
		"hoist-keep/main-stack:" + curJob + "-db", "hoist-keep/main-stack:" + curJob + "-web",
	}
	if !slices.Equal(f.tags, want) {
		t.Errorf("tags = %v", f.tags)
	}
}

func TestBaseline(t *testing.T) {
	e := newEnv(t, false)
	var f fakeImages
	f.install(e)
	e.do("GET", "/api/stacks/main-stack", "")
	recs, err := e.srv.Audit.Deploys(t.Context(), "main-stack", 10)
	if err != nil || len(recs) != 1 || recs[0].Trigger != "baseline" || len(recs[0].Images) != 2 {
		t.Fatalf("baseline = %+v, %v", recs, err)
	}
	web := recs[0].Images[1]
	if web.Service != "web" || web.Digest != "sha256:new-d" || web.Revision != "bbb" || web.Source != "https://github.com/a/web" {
		t.Errorf("web = %+v", web)
	}
}

func TestRollbackAndResume(t *testing.T) {
	e := newEnv(t, false)
	var f fakeImages
	f.install(e)
	withHistory(t, e)

	plan := decode[rollbackPlan](t, e.do("GET", "/api/stacks/main-stack/rollback", ""))
	if plan.Target.Job != oldJob || plan.Problem != "" || len(plan.Services) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	db, web := plan.Services[0], plan.Services[1]
	if db.Changes || !web.Changes || web.Pinned != "ghcr.io/a/web@sha256:old-d" || web.Where != "host" || web.From.Revision != "bbb" {
		t.Errorf("services = %+v", plan.Services)
	}

	w := e.do("POST", "/api/stacks/main-stack/rollback", `{"to":"`+oldJob+`"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("rollback: %d %s", w.Code, w.Body)
	}
	job := waitJob(t, e, decode[jobs.Job](t, w).ID)
	if job.State != jobs.Done || !job.Pinned || job.Rollback != oldJob {
		t.Fatalf("job = %+v", job)
	}
	log, _ := os.ReadFile(e.srv.Jobs.LogPath(job.ID))
	if !strings.Contains(string(log), "not pulling") || strings.Contains(string(log), "$ docker compose pull") {
		t.Errorf("a pinned deploy pulled:\n%s", log)
	}
	info := decode[StackInfo](t, e.do("GET", "/api/stacks/main-stack", ""))
	if info.Pin == nil || info.Pin.Deploy != oldJob || info.Pin.Images["web"] != "ghcr.io/a/web@sha256:old-d" {
		t.Fatalf("pin = %+v", info.Pin)
	}
	st, _ := e.srv.Config.Stack("main-stack")
	override, err := os.ReadFile(st.Pin.Override)
	if err != nil || !strings.Contains(string(override), "image: ghcr.io/a/web@sha256:old-d") {
		t.Fatalf("override = %s, %v", override, err)
	}
	events, _ := e.srv.Audit.List(t.Context(), audit.Query{Action: audit.Rollback})
	if len(events) != 1 || events[0].Job != job.ID || events[0].Result != audit.OK || !strings.HasPrefix(events[0].Detail, "to the deploy of") {
		t.Errorf("audit = %+v", events)
	}

	w = e.do("POST", "/api/stacks/main-stack/resume", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	job = waitJob(t, e, decode[jobs.Job](t, w).ID)
	if job.Pinned || job.State != jobs.Done {
		t.Errorf("resume job = %+v", job)
	}
	if info := decode[StackInfo](t, e.do("GET", "/api/stacks/main-stack", "")); info.Pin != nil {
		t.Errorf("still pinned: %+v", info.Pin)
	}
	if _, err := os.Stat(filepath.Dir(st.Pin.Override)); !os.IsNotExist(err) {
		t.Errorf("pin files left behind: %v", err)
	}
	if w := e.do("POST", "/api/stacks/main-stack/resume", ""); w.Code != http.StatusConflict {
		t.Errorf("resume when not pinned: %d", w.Code)
	}
}

func TestRollbackRefusesMissingImage(t *testing.T) {
	e := newEnv(t, false)
	var f fakeImages
	f.install(e)
	withHistory(t, e)
	hook := *e.docker
	*e.docker = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/images/ghcr.io/a/web@sha256:old-d/json" {
			return false // not on the host
		}
		return hook(w, r)
	}
	// Nor is it in the registry.
	reg := httptest.NewTLSServer(http.NotFoundHandler())
	defer reg.Close()
	host := strings.TrimPrefix(reg.URL, "https://")
	e.srv.registry = registry.NewWith(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.URL.Host = host
		return reg.Client().Transport.RoundTrip(r)
	})})
	plan := decode[rollbackPlan](t, e.do("GET", "/api/stacks/main-stack/rollback", ""))
	if !strings.Contains(plan.Problem, "web's image is gone") {
		t.Errorf("problem = %q", plan.Problem)
	}
	if w := e.do("POST", "/api/stacks/main-stack/rollback", ""); w.Code != http.StatusConflict {
		t.Errorf("rollback without the image: %d %s", w.Code, w.Body)
	}
	if st, _ := e.srv.Config.Stack("main-stack"); st.Pin != nil {
		t.Error("a refused rollback pinned the stack")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
