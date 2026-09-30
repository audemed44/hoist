package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"nginx":                           {Registry: dockerHub, Repo: "library/nginx", Tag: "latest"},
		"nginx:alpine":                    {Registry: dockerHub, Repo: "library/nginx", Tag: "alpine"},
		"louislam/uptime-kuma:2":          {Registry: dockerHub, Repo: "louislam/uptime-kuma", Tag: "2"},
		"docker.io/library/mongo:7":       {Registry: dockerHub, Repo: "library/mongo", Tag: "7"},
		"ghcr.io/audemed44/hoist:latest":  {Registry: "ghcr.io", Repo: "audemed44/hoist", Tag: "latest"},
		"lscr.io/linuxserver/qbittorrent": {Registry: "lscr.io", Repo: "linuxserver/qbittorrent", Tag: "latest"},
		"localhost:5000/app:1.0":          {Registry: "localhost:5000", Repo: "app", Tag: "1.0"},
		"ghcr.io/a/b@sha256:abc":          {Registry: "ghcr.io", Repo: "a/b", Digest: "sha256:abc"},
		"ghcr.io/a/b:1.2@sha256:abc":      {Registry: "ghcr.io", Repo: "a/b", Tag: "1.2", Digest: "sha256:abc"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", in, got, err)
		}
	}
	r, _ := Parse("nginx:1")
	if r.Name() != "nginx" {
		t.Errorf("Name = %q", r.Name())
	}
}

func TestNewer(t *testing.T) {
	tags := []string{"latest", "0.4.1", "0.4.2", "0.5.0", "0.5.0-rc1", "1.0.0", "v0.6.0", "0.4", "0.6.0-alpine", "0.3.9"}
	cur, _ := ParseVersion("0.4.1")
	var got []string
	for _, v := range Newer(cur, tags) {
		got = append(got, v.Tag+"/"+cur.Bump(v))
	}
	if strings.Join(got, " ") != "0.4.2/patch 0.5.0/minor 1.0.0/major" {
		t.Errorf("newer = %v", got)
	}

	ls, _ := ParseVersion("5.0.1-ls300")
	var lsGot []string
	for _, v := range Newer(ls, []string{"5.0.1-ls299", "5.0.1-ls301", "5.1.0-ls1", "5.1.0"}) {
		lsGot = append(lsGot, v.Tag+"/"+ls.Bump(v))
	}
	if strings.Join(lsGot, " ") != "5.0.1-ls301/patch 5.1.0-ls1/minor" {
		t.Errorf("linuxserver newer = %v", lsGot)
	}

	if _, ok := ParseVersion("latest"); ok {
		t.Error("latest parsed as a version")
	}
	if !Allows("minor", "patch") || Allows("patch", "minor") || Allows("minor", "major") || Allows("off", "patch") {
		t.Error("Allows")
	}
}

// A fake registry that wants a token, like ghcr.io and Docker Hub.
func TestClient(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:me/app:pull" {
				http.Error(w, "bad scope", 400)
				return
			}
			fmt.Fprint(w, `{"token":"t0k","expires_in":300}`)
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="fake",scope="repository:me/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/me/app/manifests/latest" && r.Method == http.MethodHead:
			w.Header().Set("Docker-Content-Digest", "sha256:new")
		case r.URL.Path == "/v2/me/app/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/me/app/tags/list?n=2&last=b>; rel="next"`)
			fmt.Fprint(w, `{"tags":["a","b"]}`)
		case r.URL.Path == "/v2/me/app/tags/list":
			fmt.Fprint(w, `{"tags":["c"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New()
	c.http = srv.Client()
	ref, _ := Parse(strings.TrimPrefix(srv.URL, "https://") + "/me/app")
	d, err := c.Digest(context.Background(), ref)
	if err != nil || d != "sha256:new" {
		t.Fatalf("digest = %q, %v", d, err)
	}
	tags, err := c.Tags(context.Background(), ref)
	if err != nil || strings.Join(tags, ",") != "a,b,c" {
		t.Fatalf("tags = %v, %v", tags, err)
	}
	missing, _ := Parse(strings.TrimPrefix(srv.URL, "https://") + "/me/app:nope")
	if _, err := c.Digest(context.Background(), missing); err != ErrNotFound {
		t.Errorf("missing tag: %v", err)
	}
}
