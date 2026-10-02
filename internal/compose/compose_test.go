package compose

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/audemed44/hoist/internal/docker"
)

func TestSummary(t *testing.T) {
	base := `
services:
  shelfloom:
    image: ghcr.io/audemed44/shelfloom:0.4
    ports: ["8000:8000"]
  yamtrack:
    image: yamtrack
  foyer:
    image: ghcr.io/audemed44/foyer:latest
`
	cases := map[string]struct{ after, want string }{
		"tag bump": {`
services:
  shelfloom:
    image: ghcr.io/audemed44/shelfloom:0.5
    ports: ["8000:8000"]
  yamtrack:
    image: yamtrack
  foyer:
    image: ghcr.io/audemed44/foyer:latest
`, "chore(main-stack): bump shelfloom 0.4 → 0.5"},
		"add remove update": {`
services:
  shelfloom:
    image: ghcr.io/audemed44/shelfloom:0.4
    ports: ["8001:8000"]
  romm:
    image: rommapp/romm
  foyer:
    image: ghcr.io/audemed44/foyer:latest
`, "feat(main-stack): add romm, remove yamtrack, update shelfloom"},
		"other repo": {`
services:
  shelfloom:
    image: ghcr.io/audemed44/shelfloom:0.4
    ports: ["8000:8000"]
  yamtrack:
    image: ghcr.io/fuzzygrim/yamtrack
  foyer:
    image: ghcr.io/audemed44/foyer:latest
`, "chore(main-stack): bump yamtrack yamtrack → ghcr.io/fuzzygrim/yamtrack"},
		"no service change": {base + "\nvolumes: {data: {}}\n", "chore(main-stack): update compose file"},
		"invalid yaml":      {"services: [", "chore(main-stack): update compose file"},
		"many": {`
services:
  a: {image: a}
  b: {image: b}
  c: {image: c}
  d: {image: d}
  e: {image: e}
`, "feat(main-stack): add a, add b, add c and 5 more"},
	}
	for name, c := range cases {
		if got := Summary("main-stack", []byte(base), []byte(c.after)); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestSplitImage(t *testing.T) {
	for in, want := range map[string][2]string{
		"nginx":                    {"nginx", "latest"},
		"nginx:1.27":               {"nginx", "1.27"},
		"localhost:5000/app":       {"localhost:5000/app", "latest"},
		"localhost:5000/app:2":     {"localhost:5000/app", "2"},
		"ghcr.io/a/b@sha256:abcd1": {"ghcr.io/a/b", "sha256:abcd1"},
	} {
		repo, tag := splitImage(in)
		if repo != want[0] || tag != want[1] {
			t.Errorf("splitImage(%q) = %q, %q", in, repo, tag)
		}
	}
}

func TestPlan(t *testing.T) {
	services := []Service{
		{Name: "same", Hash: "h1"},
		{Name: "changed", Hash: "h2"},
		{Name: "stopped", Hash: "h3"},
		{Name: "new", Hash: "h4"},
	}
	containers := []docker.Container{
		{Service: "same", ConfigHash: "h1", State: "running"},
		{Service: "changed", ConfigHash: "old", State: "running"},
		{Service: "stopped", ConfigHash: "h3", State: "exited"},
		{Service: "gone", ConfigHash: "x", State: "running"},
		{Service: "new", ConfigHash: "zzz", State: "exited", OneOff: true}, // `compose run` leftovers don't count
	}
	changes := func(plan []ServiceState) map[string]Change {
		m := map[string]Change{}
		for _, s := range plan {
			m[s.Name] = s.Change
		}
		return m
	}
	want := map[string]Change{"same": Unchanged, "changed": Recreate, "stopped": Start, "new": Create, "gone": Remove}
	if got := changes(Plan(services, containers, true)); !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v", got)
	}
	want["gone"] = Unchanged
	if got := changes(Plan(services, containers, false)); !reflect.DeepEqual(got, want) {
		t.Errorf("plan without orphan removal = %v", got)
	}
}

func TestConventional(t *testing.T) {
	for msg, want := range map[string]bool{
		"feat(main-stack): add romm":               true,
		"chore: tidy":                              true,
		"fix(kopia)!: move the repository\n\nbody": true,
		"refactor(a/b.c): x":                       true,
		"main-stack: add romm":                     false,
		"Feat(x): y":                               false,
		"feat(x):y":                                false,
		"feat(Main): y":                            false,
		"wip":                                      false,
		"":                                         false,
	} {
		if got := Conventional(msg); got != want {
			t.Errorf("Conventional(%q) = %v", msg, got)
		}
	}
}

func TestEnvironKeepsHoistSettingsOut(t *testing.T) {
	t.Setenv("HOME", "/tmp")
	t.Setenv("HOIST_HOST_HOME", "/home/u")
	t.Setenv("HOIST_TOKEN", "secret")
	t.Setenv("TZ", "Europe/London")
	t.Setenv("DOCKER_HOST", "unix:///x.sock")
	env := environ()
	for _, bad := range []string{"HOIST_TOKEN=secret", "TZ=Europe/London", "HOME=/tmp"} {
		if slices.Contains(env, bad) {
			t.Errorf("environ passes %s", bad)
		}
	}
	for _, want := range []string{"HOME=/home/u", "DOCKER_HOST=unix:///x.sock"} {
		if !slices.Contains(env, want) {
			t.Errorf("environ lacks %s: %v", want, env)
		}
	}
}

func TestLineEndings(t *testing.T) {
	crlf := []byte("services:\r\n  a: {}\r\n")
	if got := string(ToLF(crlf)); got != "services:\n  a: {}\n" {
		t.Errorf("ToLF = %q", got)
	}
	if !UsesCRLF(crlf) || UsesCRLF(ToLF(crlf)) || UsesCRLF(nil) {
		t.Error("UsesCRLF")
	}
}

func TestBumpImage(t *testing.T) {
	file := []byte("services:\n  a:\n    image: \"ghcr.io/me/a:0.4.1\" # pinned\n  b:\n    image: nginx:1.27\n  c:\n    image: app:${TAG}\n")
	out, ref, err := BumpImage(file, "ghcr.io/me/a:0.4.1", "0.5.0")
	if err != nil || ref != "ghcr.io/me/a:0.5.0" || !strings.Contains(string(out), `image: "ghcr.io/me/a:0.5.0" # pinned`) {
		t.Fatalf("bump = %q, %q, %v", out, ref, err)
	}
	if strings.Count(string(out), "0.4.1") != 0 || !strings.Contains(string(out), "nginx:1.27") {
		t.Errorf("changed too much: %s", out)
	}
	if _, _, err := BumpImage(file, "app:1.0", "1.1"); err == nil {
		t.Error("an image set by a variable should be refused")
	}
	twice := []byte("services:\n  a: {image: x}\n  b:\n    image: nginx:1\n  c:\n    image: nginx:1\n")
	if _, _, err := BumpImage(twice, "nginx:1", "2"); err == nil {
		t.Error("an image on two lines should be refused")
	}
	if WithTag("localhost:5000/app", "2") != "localhost:5000/app:2" {
		t.Error("WithTag with a registry port")
	}
}

func TestParsePorts(t *testing.T) {
	svcs, err := parseServices([]byte(`{"services":{"a":{"image":"nginx","ports":[
		{"target":80,"published":"8080","protocol":"tcp"},
		{"host_ip":"127.0.0.1","target":53,"published":"5353","protocol":"udp"},
		{"target":3000}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	ports := svcs[0].Ports
	if len(ports) != 2 || ports[0].String() != "8080/tcp" || ports[1].String() != "127.0.0.1:5353/udp" {
		t.Fatalf("ports = %v", ports)
	}
	for _, tc := range []struct {
		a, b Port
		want bool
	}{
		{Port{"", 80, "tcp"}, Port{"127.0.0.1", 80, "tcp"}, true},
		{Port{"::", 80, "tcp"}, Port{"10.0.0.1", 80, "tcp"}, true},
		{Port{"127.0.0.1", 80, "tcp"}, Port{"127.0.0.2", 80, "tcp"}, false},
		{Port{"", 80, "tcp"}, Port{"", 80, "udp"}, false},
		{Port{"", 80, "tcp"}, Port{"", 81, "tcp"}, false},
	} {
		if got := tc.a.Overlaps(tc.b); got != tc.want {
			t.Errorf("%v overlaps %v = %v", tc.a, tc.b, got)
		}
	}
}

func TestLint(t *testing.T) {
	raw := []byte(`services:
  app:
    image: ghcr.io/audemed44/foyer:latest
    restart: unless-stopped
    environment:
      DB_PASSWORD: hunter2
      API_KEY: ${API_KEY}
      TOKEN_FILE: /run/secrets/token
      DEBUG: "true"
  db:
    image: postgres
    environment:
      - POSTGRES_PASSWORD=s3cret
      - POSTGRES_USER=app
  job:
    build: .
    healthcheck:
      disable: true
`)
	resolved, err := parseServices([]byte(`{"services":{
		"app":{"image":"ghcr.io/audemed44/foyer:latest","restart":"unless-stopped"},
		"db":{"image":"postgres","deploy":{"restart_policy":{"condition":"on-failure"}}},
		"job":{"image":"job","build":{"context":"."},"healthcheck":{"disable":true}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	hints := Lint(LintInput{
		Raw: raw, Services: resolved, Own: OwnImages("AudeMed44"),
		ImageHealthcheck: func(image string) (bool, bool) { return image == "postgres", image != "ghcr.io/audemed44/foyer:latest" },
	})
	var got []string
	for _, h := range hints {
		got = append(got, h.Service+" "+h.Kind)
		if strings.Contains(h.Message, "hunter2") || strings.Contains(h.Message, "s3cret") {
			t.Errorf("hint repeats a secret: %s", h.Message)
		}
	}
	want := "app healthcheck,db latest,job restart,app secret,db secret"
	if strings.Join(got, ",") != want {
		t.Errorf("hints = %v, want %s", got, want)
	}
	if !strings.Contains(hints[0].Message, "isn't pulled yet") {
		t.Errorf("unknown image healthcheck: %s", hints[0].Message)
	}
	if !isLatest("nginx") || !isLatest("localhost:5000/app") || isLatest("nginx:1.27") || isLatest("nginx@sha256:abc") {
		t.Error("isLatest")
	}
}

func TestMarkAsleep(t *testing.T) {
	states := []ServiceState{
		{Name: "convertx", Change: Start, Container: &docker.Container{Name: "convertx", State: "exited"}},
		{Name: "bentopdf", Change: Recreate, Container: &docker.Container{Name: "bentopdf", State: "exited"}},
		{Name: "manual", Change: Start, Container: &docker.Container{Name: "manual", State: "exited"}},
		{Name: "awake", Container: &docker.Container{Name: "awake", State: "running"}},
	}
	MarkAsleep(states, map[string]string{"convertx": "sleeping", "bentopdf": "sleeping", "awake": "waking"})
	if states[0].Asleep != "sleeping" || states[0].Change != Unchanged {
		t.Fatalf("sleeping: %+v", states[0])
	}
	if states[1].Asleep != "sleeping" || states[1].Change != Recreate {
		t.Fatalf("a recreate still has to happen: %+v", states[1])
	}
	if states[2].Asleep != "" || states[2].Change != Start {
		t.Fatalf("stopped by hand: %+v", states[2])
	}
	if states[3].Asleep != "" {
		t.Fatalf("running: %+v", states[3])
	}
}
