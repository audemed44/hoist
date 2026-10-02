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

func TestPinnable(t *testing.T) {
	for want, tags := range map[string][]string{
		"1.29.1":     {"latest", "1.29.1", "1.29.0", "1.28", "1.29.2-alpine", "1.30.0-rc1", "mainline", "1.27.5"},
		"5.1.0-ls12": {"latest", "5.0.1-ls300", "5.1.0-ls12", "5.1.0-ls9", "develop"},
		"":           {"latest", "stable"},
		"v2.3.0":     {"v2.3.0", "v2.2.9", "v2.3.0-beta"},
	} {
		if got := Pinnable(tags); got != want {
			t.Errorf("Pinnable(%v) = %q, want %q", tags, got, want)
		}
	}
}

func TestConfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/me/app/manifests/1.0":
			fmt.Fprint(w, `{"manifests":[
				{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},
				{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}},
				{"digest":"sha256:att","platform":{"os":"unknown","architecture":"unknown"}}]}`)
		case "/v2/me/app/manifests/sha256:amd", "/v2/me/app/manifests/sha256:arm":
			fmt.Fprint(w, `{"config":{"digest":"sha256:cfg"}}`)
		case "/v2/me/app/blobs/sha256:cfg":
			fmt.Fprint(w, `{"config":{"ExposedPorts":{"8080/tcp":{},"443/tcp":{},"53/udp":{}},
				"Volumes":{"/data":{},"/config":{}},"Healthcheck":{"Test":["CMD","true"]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New()
	c.http = srv.Client()
	ref, _ := Parse(strings.TrimPrefix(srv.URL, "https://") + "/me/app:1.0")
	cfg, err := c.Config(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Ports, ",") != "53/udp,443/tcp,8080/tcp" || strings.Join(cfg.Volumes, ",") != "/config,/data" || !cfg.Healthcheck {
		t.Errorf("config = %+v", cfg)
	}
}

func TestDigestForAndPinned(t *testing.T) {
	digests := []string{"ghcr.io/a/web@sha256:1", "nginx@sha256:2"}
	for _, c := range []struct{ ref, wantRef, want string }{
		{"ghcr.io/a/web:latest", "ghcr.io/a/web:latest", "sha256:1"},
		{"nginx:1.27", "nginx:1.27", "sha256:2"},
		{"docker.io/library/nginx", "docker.io/library/nginx", "sha256:2"},
		{"ghcr.io/a/other:latest", "ghcr.io/a/other:latest", ""},
		{"ghcr.io/a/web@sha256:9", "ghcr.io/a/web@sha256:9", "sha256:9"},
		// The tag moved on, so docker shows the image ID.
		{"sha256:abc", "ghcr.io/a/web", "sha256:1"},
	} {
		ref, d := DigestFor(c.ref, digests)
		if ref != c.wantRef || d != c.want {
			t.Errorf("DigestFor(%s) = %s, %s", c.ref, ref, d)
		}
	}
	for ref, want := range map[string]string{
		"ghcr.io/a/web:latest":    "ghcr.io/a/web@sha256:1",
		"nginx:1.27":              "nginx@sha256:1",
		"linuxserver/kopia:0.1":   "linuxserver/kopia@sha256:1",
		"ghcr.io/a/web@sha256:00": "ghcr.io/a/web@sha256:1",
	} {
		if got := Pinned(ref, "sha256:1"); got != want {
			t.Errorf("Pinned(%s) = %s", ref, got)
		}
	}
}
