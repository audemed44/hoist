package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/updates"
)

const token = "0123456789abcdef0123"

// fakeCompose stands in for docker-compose: it validates unless the file
// says INVALID, and resolves to two services, web and db.
const fakeCompose = `#!/bin/sh
file=""
while [ $# -gt 0 ]; do
  case "$1" in
    --file) file="$2"; shift 2 ;;
    --project-name|--project-directory|--ansi|--progress) shift 2 ;;
    *) break ;;
  esac
done
case "$1 $2" in
  "config --quiet") if grep -q INVALID "$file"; then echo "invalid compose file $file" >&2; exit 1; fi ;;
  "config --format") echo '{"services":{"web":{"image":"nginx"},"db":{"image":"postgres"}}}' ;;
  "config --hash") printf 'web h-web\ndb h-db\n' ;;
  *) echo "compose $*" ;;
esac
`

type env struct {
	t     *testing.T
	srv   *Server
	h     http.Handler
	stack config.Stack
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newEnv(t *testing.T, readOnly bool) *env {
	t.Helper()
	root := t.TempDir()

	bin := filepath.Join(root, "docker-compose")
	if err := os.WriteFile(bin, []byte(fakeCompose), 0o755); err != nil {
		t.Fatal(err)
	}
	old := compose.Bin
	compose.Bin = bin
	t.Cleanup(func() { compose.Bin = old })

	// The stack lives in a clone of a bare "GitHub" repo.
	remote := filepath.Join(root, "remote.git")
	git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	dir := filepath.Join(root, "main-stack")
	git(t, root, "clone", "-q", remote, dir)
	_ = os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  web:\n    image: nginx\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("# secrets\nTZ=UTC\nDB_PASSWORD=hunter2\n"), 0o600)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "first")
	git(t, dir, "push", "-q", "-u", "origin", "main")

	sock := filepath.Join(root, "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	dockerSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "1", "Names": []string{"/web"}, "State": "running", "Labels": map[string]string{
				"com.docker.compose.service": "web", "com.docker.compose.config-hash": "h-web"}},
			{"Id": "2", "Names": []string{"/db"}, "State": "running", "Labels": map[string]string{
				"com.docker.compose.service": "db", "com.docker.compose.config-hash": "old"}},
		})
	})}
	go func() { _ = dockerSrv.Serve(l) }()
	t.Cleanup(func() { _ = dockerSrv.Close() })

	cfg := &config.Config{
		Git:    config.Git{Name: "Hoist", Email: "hoist@test"},
		Stacks: []config.Stack{{Name: "main-stack", Path: dir, File: "docker-compose.yml", Project: "main-server"}},
	}
	store, err := jobs.NewStore(filepath.Join(root, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOIST_CONTAINER", "not-in-docker")
	srv := New(Options{
		Config: cfg, Docker: docker.New(sock), Jobs: store, Token: token, ReadOnly: readOnly,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
	})
	return &env{t: t, srv: srv, h: srv.Handler(), stack: cfg.Stacks[0]}
}

func (e *env) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func TestAuth(t *testing.T) {
	e := newEnv(t, false)
	if w := e.do("GET", "/api/stacks", "", "Authorization", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := e.do("GET", "/api/stacks", "", "Authorization", "Bearer wrong"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := e.do("GET", "/api/stacks", ""); w.Code != http.StatusOK {
		t.Errorf("bearer: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/session", `{"token":"nope"}`, "Authorization", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("bad login: %d", w.Code)
	}
	w := e.do("POST", "/api/session", `{"token":"`+token+`"}`, "Authorization", "")
	cookies := w.Result().Cookies()
	if w.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].HttpOnly || strings.Contains(cookies[0].Value, token) {
		t.Fatalf("login: %d %v", w.Code, cookies)
	}
	w = e.do("GET", "/api/stacks", "", "Authorization", "", "Cookie", cookieName+"="+cookies[0].Value)
	if w.Code != http.StatusOK {
		t.Errorf("cookie: %d", w.Code)
	}
	w = e.do("GET", "/api/session", "", "Authorization", "")
	if s := decode[sessionInfo](t, w); s.Authenticated {
		t.Error("session without credentials says authenticated")
	}
}

func TestCrossOriginRefused(t *testing.T) {
	e := newEnv(t, false)
	for _, h := range [][]string{
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"}, // another subdomain
		{"Origin", "https://evil.example"},
	} {
		if w := e.do("POST", "/api/stacks/main-stack/deploy", "", h...); w.Code != http.StatusForbidden {
			t.Errorf("%v: %d", h, w.Code)
		}
	}
}

func TestReadOnly(t *testing.T) {
	e := newEnv(t, true)
	if w := e.do("PUT", "/api/stacks/main-stack/compose", `{"content":"services: {}"}`); w.Code != http.StatusForbidden {
		t.Errorf("save: %d", w.Code)
	}
	if w := e.do("POST", "/api/stacks/main-stack/deploy", ""); w.Code != http.StatusForbidden {
		t.Errorf("deploy: %d", w.Code)
	}
	if w := e.do("GET", "/api/stacks/main-stack", ""); w.Code != http.StatusOK {
		t.Errorf("read: %d", w.Code)
	}
	widget := decode[foyerWidget](t, e.do("GET", "/api/foyer/widget", ""))
	if widget.Items[0].Action != nil {
		t.Error("read-only widget offers a deploy")
	}
}

func TestStackPlan(t *testing.T) {
	e := newEnv(t, false)
	info := decode[StackInfo](t, e.do("GET", "/api/stacks/main-stack", ""))
	if info.Error != "" || info.Counts != (Counts{Services: 2, Running: 2, Pending: 1}) {
		t.Fatalf("info = %+v", info)
	}
	changes := map[string]compose.Change{}
	for _, s := range info.Services {
		changes[s.Name] = s.Change
	}
	if changes["db"] != compose.Recreate || changes["web"] != compose.Unchanged {
		t.Errorf("changes = %v", changes)
	}
	if info.Git == nil || info.Git.Branch != "main" || info.Git.Modified {
		t.Errorf("git = %+v (%s)", info.Git, info.GitError)
	}
	if w := e.do("GET", "/api/stacks/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown stack: %d", w.Code)
	}
}

func TestSaveCompose(t *testing.T) {
	e := newEnv(t, false)
	cur := decode[composeFile](t, e.do("GET", "/api/stacks/main-stack/compose", ""))

	body := func(content, base string) string {
		b, _ := json.Marshal(map[string]string{"content": content, "base": base})
		return string(b)
	}
	if w := e.do("PUT", "/api/stacks/main-stack/compose", body("services: {}\n", "stale")); w.Code != http.StatusConflict {
		t.Errorf("stale base: %d", w.Code)
	}
	w := e.do("PUT", "/api/stacks/main-stack/compose", body("INVALID\n", cur.Hash))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "docker-compose.yml") ||
		strings.Contains(w.Body.String(), ".hoist-validate") {
		t.Errorf("invalid: %d %s", w.Code, w.Body)
	}

	next := "services:\n  web:\n    image: nginx:1.27\n"
	check := decode[map[string]string](t, e.do("POST", "/api/stacks/main-stack/check", body(next, "")))
	if check["message"] != "chore(main-stack): bump web latest → 1.27" || check["error"] != "" {
		t.Errorf("check = %v", check)
	}
	bad, _ := json.Marshal(map[string]string{"content": next, "base": cur.Hash, "message": "bump nginx"})
	if w := e.do("PUT", "/api/stacks/main-stack/compose", string(bad)); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("non-conventional message: %d", w.Code)
	}
	w = e.do("PUT", "/api/stacks/main-stack/compose", body(next, cur.Hash))
	res := decode[saveResult](t, w)
	if w.Code != http.StatusOK || res.Commit == "" || !res.Pushed {
		t.Fatalf("save: %d %+v", w.Code, res)
	}
	data, _ := os.ReadFile(e.stack.ComposePath())
	if string(data) != next {
		t.Errorf("file = %q", data)
	}
	history := decode[[]map[string]any](t, e.do("GET", "/api/stacks/main-stack/history", ""))
	if len(history) != 2 || history[0]["subject"] != "chore(main-stack): bump web latest → 1.27" || history[0]["author"] != "Hoist" {
		t.Errorf("history = %v", history)
	}
	old := decode[composeFile](t, e.do("GET", "/api/stacks/main-stack/history/"+history[1]["hash"].(string), ""))
	if old.Content != cur.Content {
		t.Errorf("old version = %q", old.Content)
	}
}

func TestEnv(t *testing.T) {
	e := newEnv(t, false)
	_ = os.WriteFile(e.stack.ComposePath(), []byte("services:\n  web:\n    environment: [TZ=${TZ}, KEY=${API_KEY}]\n"), 0o644)
	w := e.do("GET", "/api/stacks/main-stack/env", "")
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("the list leaks values")
	}
	got := decode[envResponse](t, w)
	if len(got.Entries) != 2 || got.Missing[0] != "API_KEY" || got.Unused[0] != "DB_PASSWORD" {
		t.Errorf("env = %+v", got)
	}
	v := decode[map[string]string](t, e.do("GET", "/api/stacks/main-stack/env/DB_PASSWORD", ""))
	if v["value"] != "hunter2" {
		t.Errorf("value = %v", v)
	}
	w = e.do("PUT", "/api/stacks/main-stack/env", `{"entries":[{"key":"TZ","value":null},{"key":"API_KEY","value":"k"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(e.stack.EnvPath())
	if string(data) != "# secrets\nTZ=UTC\nAPI_KEY=k\n" {
		t.Errorf(".env = %q", data)
	}
	if w := e.do("PUT", "/api/stacks/main-stack/env", `{"entries":[{"key":"bad key","value":"x"}]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad key: %d", w.Code)
	}
}

func TestDeployAndLog(t *testing.T) {
	e := newEnv(t, false)
	w := e.do("POST", "/api/stacks/main-stack/deploy", `{"services":["nope"]}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown service: %d", w.Code)
	}
	w = e.do("POST", "/api/foyer/deploy/main-stack", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", w.Code, w.Body)
	}
	started := decode[map[string]string](t, w)
	id := strings.TrimPrefix(started["status_url"], "/api/foyer/jobs/")

	// The log streams until the job ends.
	log := e.do("GET", "/api/jobs/"+id+"/log", "")
	body, _ := io.ReadAll(log.Body)
	for _, want := range []string{"$ docker compose pull --ignore-buildable", "$ docker compose up --detach --remove-orphans", "✓ Nothing changed"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("log lacks %q:\n%s", want, body)
		}
	}
	status := decode[map[string]string](t, e.do("GET", "/api/foyer/jobs/"+id, ""))
	if status["state"] != "done" || status["message"] != "main-stack: Nothing changed" {
		t.Errorf("status = %v", status)
	}
	job := decode[jobs.Job](t, e.do("GET", "/api/jobs/"+id, ""))
	if job.Trigger != "foyer" || job.Commit == "" {
		t.Errorf("job = %+v", job)
	}
}

func TestComposeLineEndings(t *testing.T) {
	e := newEnv(t, false)
	_ = os.WriteFile(e.stack.ComposePath(), []byte("services:\r\n  web:\r\n    image: nginx\r\n"), 0o644)
	cur := decode[composeFile](t, e.do("GET", "/api/stacks/main-stack/compose", ""))
	if !cur.CRLF || strings.Contains(cur.Content, "\r") {
		t.Fatalf("editor gets %+v", cur)
	}
	body, _ := json.Marshal(map[string]string{"content": "services:\r\n  web:\r\n    image: nginx:1.27\r\n", "base": cur.Hash})
	if w := e.do("PUT", "/api/stacks/main-stack/compose", string(body)); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(e.stack.ComposePath())
	if string(data) != "services:\n  web:\n    image: nginx:1.27\n" {
		t.Errorf("saved %q", data)
	}
}

// withUpdates gives the server a checker primed with a stored result.
func withUpdates(t *testing.T, e *env, state updates.State) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "updates.json")
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e.srv.Updates = updates.New(e.srv.Config, e.srv.Docker, path)
}

func waitJob(t *testing.T, e *env, id string) jobs.Job {
	t.Helper()
	for range 100 {
		j := decode[jobs.Job](t, e.do("GET", "/api/jobs/"+id, ""))
		if j.State != jobs.Running {
			return j
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job didn't finish")
	return jobs.Job{}
}

func TestApplyUpdate(t *testing.T) {
	e := newEnv(t, false)
	now := time.Now()
	withUpdates(t, e, updates.State{CheckedAt: &now, Stacks: map[string][]updates.Service{"main-stack": {
		{Service: "web", Image: "nginx", Policy: "off", Latest: &updates.Candidate{Tag: "1.27", Bump: "major"}},
	}}})
	info := decode[StackInfo](t, e.do("GET", "/api/stacks/main-stack", ""))
	if info.Counts.Updates != 1 || info.Updates[0].Latest.Tag != "1.27" {
		t.Fatalf("stack updates = %+v", info.Updates)
	}
	if w := e.do("POST", "/api/stacks/main-stack/services/web/update", `{"tag":"9.9"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a tag the check didn't find: %d", w.Code)
	}
	w := e.do("POST", "/api/stacks/main-stack/services/web/update", `{"tag":"1.27"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	job := waitJob(t, e, decode[jobs.Job](t, w).ID)
	if job.State != jobs.Done || len(job.Services) != 1 || job.Services[0] != "web" || job.Commit == "" {
		t.Errorf("job = %+v", job)
	}
	data, _ := os.ReadFile(e.stack.ComposePath())
	if !strings.Contains(string(data), "image: nginx:1.27") {
		t.Errorf("compose file = %s", data)
	}
	history := decode[[]map[string]any](t, e.do("GET", "/api/stacks/main-stack/history", ""))
	if history[0]["subject"] != "chore(main-stack): bump web latest → 1.27" {
		t.Errorf("commit = %v", history[0]["subject"])
	}
	if info := decode[StackInfo](t, e.do("GET", "/api/stacks/main-stack", "")); info.Counts.Updates != 0 {
		t.Errorf("the applied update is still offered: %+v", info.Updates)
	}
}

func TestAutoUpdate(t *testing.T) {
	e := newEnv(t, false)
	notified := make(chan map[string]string, 4)
	apprise := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		notified <- body
	}))
	defer apprise.Close()
	e.srv.Config.Updates.Notify = apprise.URL
	state := updates.State{Stacks: map[string][]updates.Service{"main-stack": {
		{Service: "web", Image: "nginx", Policy: "minor", Latest: &updates.Candidate{Tag: "2.0", Bump: "major"},
			Allowed: &updates.Candidate{Tag: "1.1", Bump: "minor"}},
		{Service: "db", Image: "postgres", Policy: "digest", NewImage: true},
		{Service: "cache", Image: "redis", Policy: "off", NewImage: true},
	}}}
	withUpdates(t, e, state)
	e.srv.autoUpdate(state)
	jobsList := decode[[]jobs.Job](t, e.do("GET", "/api/jobs?stack=main-stack", ""))
	if len(jobsList) != 1 || jobsList[0].Trigger != "auto" || strings.Join(jobsList[0].Services, ",") != "db,web" {
		t.Fatalf("jobs = %+v", jobsList)
	}
	waitJob(t, e, jobsList[0].ID)
	data, _ := os.ReadFile(e.stack.ComposePath())
	if !strings.Contains(string(data), "image: nginx:1.1") {
		t.Errorf("web wasn't bumped within its policy: %s", data)
	}
	select {
	case n := <-notified:
		if n["type"] != "success" || n["title"] != "Hoist: updated main-stack" {
			t.Errorf("notification = %v", n)
		}
	case <-time.After(10 * time.Second):
		t.Error("no notification")
	}
}

func TestDrift(t *testing.T) {
	e := newEnv(t, false)
	edited := "services:\n  web:\n    image: nginx:1.27\n"
	_ = os.WriteFile(e.stack.ComposePath(), []byte(edited), 0o644)
	d := decode[driftResponse](t, e.do("GET", "/api/stacks/main-stack/drift", ""))
	if d.Current.Content != edited || !strings.Contains(d.Committed.Content, "image: nginx\n") ||
		d.Message != "chore(main-stack): bump web latest → 1.27" {
		t.Fatalf("drift = %+v", d)
	}
	// A deploy now records that it included uncommitted changes.
	job := decode[jobs.Job](t, e.do("POST", "/api/stacks/main-stack/deploy", ""))
	if !job.Dirty {
		t.Errorf("job = %+v", job)
	}
	waitJob(t, e, job.ID)
	if w := e.do("POST", "/api/stacks/main-stack/git/discard", ""); w.Code != http.StatusOK {
		t.Fatalf("discard: %d %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(e.stack.ComposePath())
	if string(data) != "services:\n  web:\n    image: nginx\n" {
		t.Errorf("after discard: %q", data)
	}
}
