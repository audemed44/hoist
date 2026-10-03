package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/github"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/releases"
)

// fakeHub is GitHub for one repository, me/app, with two open pull
// requests: #5 ready to merge, #6 with a failed check (run 66).
type fakeHub struct {
	mu    sync.Mutex
	calls []string
	// building is set once something was merged: the image build runs.
	merged bool
}

func (f *fakeHub) called(c string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, c)
}

func (f *fakeHub) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
		merged := f.merged
		f.mu.Unlock()
		pr := func(n int, sha string) string {
			return fmt.Sprintf(`{"number":%d,"title":"pr %d","head":{"ref":"feat/%d","sha":%q,"repo":{"full_name":"me/app"}},"base":{"repo":{"full_name":"me/app"}},"user":{"login":"me"},"mergeable":true,"rebaseable":true,"mergeable_state":"clean"}`, n, n, n, sha)
		}
		run := func(id int, sha, conclusion string) string {
			return fmt.Sprintf(`{"id":%d,"name":"test","workflow_id":1,"head_sha":%q,"status":"completed","conclusion":%q,"created_at":"2026-10-01T10:00:00Z"}`, id, sha, conclusion)
		}
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /user":
			fmt.Fprint(w, `{"login":"me"}`)
		case "GET /repos/me/app":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case "GET /repos/me/app/branches/main":
			if merged {
				fmt.Fprint(w, `{"commit":{"sha":"m1"}}`)
			} else {
				fmt.Fprint(w, `{"commit":{"sha":"r1"}}`)
			}
		case "GET /repos/me/app/compare/r1...m1":
			fmt.Fprint(w, `{"status":"ahead","ahead_by":1,"commits":[{"sha":"m1","commit":{"message":"pr 5"}}]}`)
		case "GET /repos/me/app/actions/workflows/docker.yml/runs":
			if merged {
				fmt.Fprint(w, `{"workflow_runs":[`+run(90, "m1", "success")+`]}`)
			} else {
				fmt.Fprint(w, `{"workflow_runs":[]}`)
			}
		case "GET /repos/me/app/pulls":
			fmt.Fprint(w, "["+pr(5, "p5")+","+pr(6, "p6")+"]")
		case "GET /repos/me/app/pulls/5":
			fmt.Fprint(w, pr(5, "p5"))
		case "GET /repos/me/app/pulls/6":
			fmt.Fprint(w, pr(6, "p6"))
		case "GET /repos/me/app/actions/runs":
			if r.URL.Query().Get("head_sha") == "p6" {
				fmt.Fprint(w, `{"workflow_runs":[`+run(66, "p6", "failure")+`]}`)
			} else {
				fmt.Fprint(w, `{"workflow_runs":[`+run(55, "p5", "success")+`]}`)
			}
		case "GET /repos/me/app/pulls/5/reviews", "GET /repos/me/app/pulls/6/reviews":
			fmt.Fprint(w, `[]`)
		case "PUT /repos/me/app/pulls/5/merge", "PUT /repos/me/app/pulls/6/merge":
			f.mu.Lock()
			f.merged = true
			f.mu.Unlock()
			fmt.Fprint(w, `{"sha":"m1","merged":true}`)
		case "DELETE /repos/me/app/git/refs/heads/feat/5", "DELETE /repos/me/app/git/refs/heads/feat/6":
			w.WriteHeader(http.StatusNoContent)
		case "POST /repos/me/app/actions/runs/66/rerun-failed-jobs":
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withBoard gives the test env a release board: the stack's web service
// runs me/app at r1.
func withBoard(t *testing.T, e *env) *fakeHub {
	t.Helper()
	hub := &fakeHub{}
	srv := hub.serve(t)
	e.srv.Releases.GitHub = github.NewWith(srv.URL, "tok", srv.Client())
	prev := *e.docker
	*e.docker = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/containers/json" && r.URL.Query().Get("filters") == "":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id": "c-web", "Names": []string{"/web"}, "Image": "localhost:1/me/app:latest", "ImageID": "sha256:app",
				"State": "running", "Labels": map[string]string{
					"com.docker.compose.project": "main-server", "com.docker.compose.service": "web",
					"org.opencontainers.image.source": "https://github.com/me/app", "org.opencontainers.image.revision": "r1"}}})
			return true
		case r.URL.Path == "/images/sha256:app/json":
			_, _ = w.Write([]byte(`{"Id":"sha256:app","RepoDigests":["localhost:1/me/app@sha256:running"]}`))
			return true
		}
		return prev != nil && prev(w, r)
	}
	return hub
}

func TestReleasesBoard(t *testing.T) {
	e := newEnv(t, false)
	withBoard(t, e)
	b := decode[releasesResponse](t, e.do("GET", "/api/releases", ""))
	if !b.Configured || len(b.Apps) != 1 || b.Apps[0].Stack != "main-stack" || len(b.Apps[0].PRs) != 2 {
		t.Fatalf("board = %+v", b)
	}
	if b.Summary.PRs != 2 || b.Summary.ToMerge != 1 || b.Summary.Failing != 1 {
		t.Errorf("summary = %+v", b.Summary)
	}
}

func TestReleaseMerge(t *testing.T) {
	e := newEnv(t, false)
	hub := withBoard(t, e)

	if w := e.do("POST", "/api/releases/merge", `{"repo":"me/app","number":6}`); w.Code != http.StatusConflict {
		t.Errorf("merge with failing checks: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/releases/merge", `{"repo":"me/app","number":5}`, "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site merge: %d", w.Code)
	}
	w := e.do("POST", "/api/releases/merge", `{"repo":"me/app","number":5}`)
	res := decode[mergeResult](t, w)
	if w.Code != http.StatusOK || res.SHA != "m1" || !res.Deleted {
		t.Fatalf("merge: %d %s", w.Code, w.Body)
	}
	if !hub.called(`PUT /repos/me/app/pulls/5/merge {"merge_method":"rebase","sha":"p5"}`) || !hub.called("DELETE /repos/me/app/git/refs/heads/feat/5 ") {
		t.Errorf("calls = %v", hub.calls)
	}
	events, _ := e.srv.Audit.List(t.Context(), audit.Query{Action: audit.ReleaseMerge})
	if len(events) != 1 || events[0].Detail != "me/app#5: pr 5" || events[0].Commit != "m1" || events[0].Trigger != "api" {
		t.Errorf("audit = %+v", events)
	}

	w = e.do("POST", "/api/releases/merge", `{"repo":"me/app","number":6,"force":true}`)
	if w.Code != http.StatusOK {
		t.Errorf("forced merge: %d %s", w.Code, w.Body)
	}
}

func TestReleaseRerunAndDeploy(t *testing.T) {
	e := newEnv(t, false)
	hub := withBoard(t, e)
	if w := e.do("POST", "/api/releases/rerun", `{"repo":"me/app","number":6}`); w.Code != http.StatusAccepted {
		t.Fatalf("rerun: %d %s", w.Code, w.Body)
	}
	if !hub.called("POST /repos/me/app/actions/runs/66/rerun-failed-jobs ") {
		t.Errorf("calls = %v", hub.calls)
	}
	if w := e.do("POST", "/api/releases/rerun", `{"repo":"me/app","number":5}`); w.Code != http.StatusConflict {
		t.Errorf("rerun with nothing failed: %d", w.Code)
	}
	w := e.do("POST", "/api/releases/deploy", `{"repo":"me/app"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", w.Code, w.Body)
	}
	job := waitJob(t, e, decode[jobs.Job](t, w).ID)
	if !slices.Equal(job.Services, []string{"web"}) || job.Trigger != "api" {
		t.Errorf("job = %+v", job)
	}
	if w := e.do("POST", "/api/releases/deploy", `{"repo":"me/nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown app: %d", w.Code)
	}
}

func TestReleasesWithoutToken(t *testing.T) {
	e := newEnv(t, false)
	if b := decode[releasesResponse](t, e.do("GET", "/api/releases", "")); b.Configured {
		t.Error("configured without a token")
	}
	if w := e.do("POST", "/api/releases/merge", `{"repo":"me/app","number":5}`); w.Code != http.StatusConflict {
		t.Errorf("merge without a token: %d", w.Code)
	}
}

func fastShips(t *testing.T) {
	old := shipPoll
	shipPoll = 20 * time.Millisecond
	t.Cleanup(func() { shipPoll = old })
}

func waitShip(t *testing.T, e *env, id string, state string) Ship {
	t.Helper()
	for range 200 {
		for _, sh := range e.srv.ships.list() {
			if sh.ID == id && sh.State == state {
				return sh
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ship %s never got to %s: %+v", id, state, e.srv.ships.list())
	return Ship{}
}

func TestShip(t *testing.T) {
	fastShips(t)
	e := newEnv(t, false)
	notified := make(chan string, 4)
	apprise := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		notified <- body["type"] + ": " + body["body"]
	}))
	defer apprise.Close()
	e.srv.Config.Releases.Notify = apprise.URL
	hub := withBoard(t, e)

	w := e.do("POST", "/api/releases/ship", `{"repo":"me/app","number":5}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ship: %d %s", w.Code, w.Body)
	}
	sh := decode[Ship](t, w)
	if sh.SHA != "m1" || sh.State != ShipBuilding {
		t.Fatalf("ship = %+v", sh)
	}
	if w := e.do("POST", "/api/releases/ship", `{"repo":"me/app","number":6,"force":true}`); w.Code != http.StatusConflict {
		t.Errorf("a second ship of the same app: %d", w.Code)
	}
	done := waitShip(t, e, sh.ID, ShipDone)
	if done.Job == "" || !strings.HasPrefix(done.Message, "Deployed") {
		t.Errorf("done = %+v", done)
	}
	job, _ := e.srv.Jobs.Get(done.Job)
	if job == nil || !slices.Equal(job.Services, []string{"web"}) {
		t.Errorf("job = %+v", job)
	}
	if !hub.called("GET /repos/me/app/actions/workflows/docker.yml/runs ") {
		t.Errorf("calls = %v", hub.calls)
	}
	select {
	case n := <-notified:
		if !strings.HasPrefix(n, "success: Deployed") {
			t.Errorf("notified %q", n)
		}
	case <-time.After(2 * time.Second):
		t.Error("no notification")
	}
	b := decode[releasesResponse](t, e.do("GET", "/api/releases", ""))
	if len(b.Apps[0].Ships) != 1 || b.Apps[0].Ships[0].State != ShipDone {
		t.Errorf("ships on the board = %+v", b.Apps[0].Ships)
	}
}

func TestShipCancel(t *testing.T) {
	fastShips(t)
	e := newEnv(t, false)
	withBoard(t, e)
	// No build ever shows up for the merge.
	e.srv.Config.Releases.Workflow = "missing.yml"
	sh := decode[Ship](t, e.do("POST", "/api/releases/ship", `{"repo":"me/app","number":5}`))
	if w := e.do("DELETE", "/api/releases/ships/"+sh.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	got := waitShip(t, e, sh.ID, ShipCancelled)
	if got.Job != "" {
		t.Errorf("a cancelled ship deployed: %+v", got)
	}
	if w := e.do("DELETE", "/api/releases/ships/"+sh.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("cancel again: %d", w.Code)
	}
}

func TestBoardEvents(t *testing.T) {
	b := &releases.Board{Apps: []releases.App{{
		ID: "me/app@s", Repo: "me/app", State: releases.Ready, Head: "h", Latest: "d", Behind: 2,
		Commits: []github.Commit{{Message: "feat: x"}},
		PRs: []releases.PR{
			{PullRequest: github.PullRequest{Number: 3, SHA: "a", Title: "t"}, State: releases.PRFailing},
			{PullRequest: github.PullRequest{Number: 4}, State: releases.PRReady},
		},
	}}}
	ev := boardEvents(b)
	if len(ev) != 2 {
		t.Fatalf("events = %+v", ev)
	}
	if e := ev["ready me/app@s h d"]; e.body != "A new image is published and waiting to be deployed: feat: x (and 1 more)" {
		t.Errorf("ready = %+v", e)
	}
	if e := ev["ci me/app 3 a"]; e.kind != "failure" || e.title != "Hoist: CI failing on app#3" {
		t.Errorf("ci = %+v", e)
	}
}

func TestFoyerReleases(t *testing.T) {
	fastShips(t)
	e := newEnv(t, false)
	hub := withBoard(t, e)
	widget := decode[foyerWidget](t, e.do("GET", "/api/foyer/widget", ""))
	var stat *foyerStat
	for i := range widget.Stats {
		if widget.Stats[i].Label == "Releases" {
			stat = &widget.Stats[i]
		}
	}
	if stat == nil || stat.Value != "1" || stat.Caption != "2 PRs open · 1 failing" || stat.Tone != "bad" {
		t.Fatalf("stat = %+v", stat)
	}
	// #5 to merge and deploy and #6 failing come before the stacks.
	if len(widget.Items) < 3 || widget.Items[0].Title != "app #5" || widget.Items[0].Action == nil || widget.Items[1].Caption != "CI failing" {
		t.Fatalf("items = %+v", widget.Items)
	}
	if a := widget.Items[0].Action; a.Label != "Merge & deploy" || !strings.HasSuffix(a.URL, "/pulls/5/ship") {
		t.Fatalf("action = %+v", a)
	}
	w := e.do("POST", widget.Items[0].Action.URL, "{}")
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "Merged me/app#5") {
		t.Fatalf("merge and deploy from Foyer: %d %s", w.Code, w.Body)
	}
	if !hub.called(`PUT /repos/me/app/pulls/5/merge {"merge_method":"rebase","sha":"p5"}`) {
		t.Errorf("calls = %v", hub.calls)
	}
	status := decode[map[string]string](t, w)["status_url"]
	var res map[string]string
	deadline := time.Now().Add(5 * time.Second)
	for {
		res = decode[map[string]string](t, e.do("GET", status, ""))
		if res["state"] != "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res["state"] != "done" || !strings.HasPrefix(res["url"], "/jobs/") || !strings.Contains(res["message"], "Deployed") {
		t.Fatalf("status = %+v", res)
	}
	if job := waitJob(t, e, strings.TrimPrefix(res["url"], "/jobs/")); job.Trigger != "foyer" {
		t.Errorf("job = %+v", job)
	}
	events, _ := e.srv.Audit.List(t.Context(), audit.Query{Action: audit.ReleaseMerge, Trigger: "foyer"})
	if len(events) != 1 {
		t.Errorf("audit = %+v", events)
	}
	// The merged pull request stays on the card as its merge and deploy.
	widget = decode[foyerWidget](t, e.do("GET", "/api/foyer/widget", ""))
	if it := widget.Items[0]; it.Title != "app #5" || !strings.HasPrefix(it.Caption, "deployed") || it.Action != nil {
		t.Errorf("shipped item = %+v", it)
	}
	if w := e.do("GET", "/api/foyer/ships/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown ship: %d", w.Code)
	}

	w = e.do("POST", "/api/foyer/releases/me/app/deploy", "{}")
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "status_url") {
		t.Fatalf("deploy from Foyer: %d %s", w.Code, w.Body)
	}
	url := decode[map[string]string](t, w)["url"]
	if job := waitJob(t, e, strings.TrimPrefix(url, "/jobs/")); job.Trigger != "foyer" {
		t.Errorf("job = %+v", job)
	}
}
