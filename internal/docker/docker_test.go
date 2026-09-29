package docker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// fakeDocker serves h on a unix socket and returns a client for it.
func fakeDocker(t *testing.T, h http.Handler) *Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return New(sock)
}

func TestProject(t *testing.T) {
	var filters string
	c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters = r.URL.Query().Get("filters")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "bbb", "Names": []string{"/web"}, "Image": "nginx", "State": "running", "Labels": map[string]string{
				labelService: "web", labelHash: "h1",
			}},
			{"Id": "aaa", "Names": []string{"/db"}, "Image": "postgres", "State": "exited", "Labels": map[string]string{
				labelService: "db", labelOneOff: "True",
			}},
		})
	}))
	got, err := c.Project(context.Background(), "main-server")
	if err != nil {
		t.Fatal(err)
	}
	if filters != `{"label":["com.docker.compose.project=main-server"]}` {
		t.Errorf("filters = %s", filters)
	}
	if len(got) != 2 || got[0].Name != "db" || !got[0].OneOff || got[1].ConfigHash != "h1" || got[1].Service != "web" {
		t.Errorf("containers = %+v", got)
	}
}

func TestFindSelfAndHelper(t *testing.T) {
	t.Setenv("HOIST_CONTAINER", "hoist")
	var created map[string]any
	c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/hoist/json":
			_, _ = w.Write([]byte(`{
				"Id": "abc", "Image": "sha256:img",
				"Config": {"User": "1000:1000", "Env": ["HOIST_TOKEN=x"], "Labels": {"com.docker.compose.project": "hoist"}},
				"HostConfig": {"Binds": ["/var/run/docker.sock:/var/run/docker.sock", "/home/u/homelab:/home/u/homelab"], "GroupAdd": ["111"]},
				"Mounts": [
					{"Type": "bind", "Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock", "RW": true},
					{"Type": "volume", "Name": "hoist_config", "Source": "/var/lib/docker/volumes/x", "Destination": "/config", "RW": true}
				]}`))
		case "/containers/create":
			if r.URL.Query().Get("name") != "hoist-job-1" {
				t.Errorf("name = %s", r.URL.Query().Get("name"))
			}
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"Id": "helper"}`))
		case "/containers/gone/json":
			http.NotFound(w, r)
		case "/containers/helper/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	self, err := c.FindSelf(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if self.Project != "hoist" || self.Image != "sha256:img" || len(self.Mounts) != 1 || self.Mounts[0].Source != "hoist_config" {
		t.Errorf("self = %+v", self)
	}
	id, err := c.RunHelper(context.Background(), self, "hoist-job-1", []string{"/hoist", "job", "1"})
	if err != nil || id != "helper" {
		t.Fatalf("helper = %q, %v", id, err)
	}
	hc := created["HostConfig"].(map[string]any)
	if created["Image"] != "sha256:img" || hc["AutoRemove"] != true || len(hc["Binds"].([]any)) != 2 ||
		len(created["Entrypoint"].([]any)) != 3 || len(created["Cmd"].([]any)) != 0 {
		t.Errorf("create body = %+v", created)
	}
	if ok, err := c.Exists(context.Background(), "hoist"); !ok || err != nil {
		t.Errorf("Exists(hoist) = %v, %v", ok, err)
	}
	if ok, err := c.Exists(context.Background(), "gone"); ok || err != nil {
		t.Errorf("Exists(gone) = %v, %v", ok, err)
	}
}
