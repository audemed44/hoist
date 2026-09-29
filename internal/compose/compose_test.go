package compose

import (
	"reflect"
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
`, "main-stack: shelfloom 0.4 → 0.5"},
		"add remove update": {`
services:
  shelfloom:
    image: ghcr.io/audemed44/shelfloom:0.4
    ports: ["8001:8000"]
  romm:
    image: rommapp/romm
  foyer:
    image: ghcr.io/audemed44/foyer:latest
`, "main-stack: add romm, remove yamtrack, update shelfloom"},
		"other repo": {`
services:
  shelfloom:
    image: ghcr.io/audemed44/shelfloom:0.4
    ports: ["8000:8000"]
  yamtrack:
    image: ghcr.io/fuzzygrim/yamtrack
  foyer:
    image: ghcr.io/audemed44/foyer:latest
`, "main-stack: yamtrack yamtrack → ghcr.io/fuzzygrim/yamtrack"},
		"no service change": {base + "\nvolumes: {data: {}}\n", "main-stack: update compose file"},
		"invalid yaml":      {"services: [", "main-stack: update compose file"},
		"many": {`
services:
  a: {image: a}
  b: {image: b}
  c: {image: c}
  d: {image: d}
  e: {image: e}
`, "main-stack: add a, add b, add c and 5 more"},
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
