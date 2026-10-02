package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Main Stack", "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "kopia", "compose.yml"), "services: {}\n")
	cfgPath := filepath.Join(dir, "hoist.yaml")
	write(t, cfgPath, `
stacks:
  - name: main-stack
    path: `+filepath.Join(dir, "Main Stack")+`
    project: main-server
  - name: kopia
    path: `+filepath.Join(dir, "kopia")+`/
    remove_orphans: false
`)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	main, _ := cfg.Stack("main-stack")
	if main.File != "docker-compose.yml" || main.Project != "main-server" || !main.ShouldRemoveOrphans() {
		t.Errorf("main-stack = %+v", main)
	}
	kopia, _ := cfg.Stack("kopia")
	if kopia.File != "compose.yml" || kopia.Project != "kopia" || kopia.ShouldRemoveOrphans() {
		t.Errorf("kopia = %+v", kopia)
	}
	if kopia.Path != filepath.Join(dir, "kopia") {
		t.Errorf("path not cleaned: %q", kopia.Path)
	}
	if cfg.Git.Name != "Hoist" || !cfg.Git.ShouldPush() {
		t.Errorf("git = %+v", cfg.Git)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "compose.yaml"), "services: {}\n")
	cases := map[string]string{
		"no stacks":     "stacks: []",
		"relative path": "stacks: [{name: a, path: a}]",
		"bad name":      "stacks: [{name: A B, path: " + dir + "/a}]",
		"duplicate":     "stacks: [{name: a, path: " + dir + "/a}, {name: a, path: " + dir + "/a}]",
		"no file":       "stacks: [{name: b, path: " + dir + "}]",
		"file escapes":  "stacks: [{name: a, path: " + dir + "/a, file: ../x.yml}]",
		"bad project":   "stacks: [{name: a, path: " + dir + "/a, project: My Project}]",
	}
	for name, content := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".yaml")
		write(t, path, content)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestProjectName(t *testing.T) {
	for in, want := range map[string]string{
		"/x/main-stack": "main-stack",
		"/x/My Stack":   "mystack",
		"/x/_hidden":    "hidden",
		"/x/a.b":        "ab",
	} {
		if got := ProjectName(in); got != want {
			t.Errorf("ProjectName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpdatePolicies(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "compose.yml"), "services: {}\n")
	path := filepath.Join(dir, "hoist.yaml")
	write(t, path, `
updates:
  every: 12h
  auto: digest
stacks:
  - name: a
    path: `+filepath.Join(dir, "a")+`
    updates:
      auto: patch
      services:
        db: "off"
  - name: b
    path: `+filepath.Join(dir, "a")+`
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Stack("a")
	b, _ := cfg.Stack("b")
	if cfg.Updates.Interval().Hours() != 12 || cfg.Policy(a, "web") != "patch" || cfg.Policy(a, "db") != "off" || cfg.Policy(b, "x") != "digest" {
		t.Errorf("interval %v, policies %s %s %s", cfg.Updates.Interval(), cfg.Policy(a, "web"), cfg.Policy(a, "db"), cfg.Policy(b, "x"))
	}
	for _, bad := range []string{"updates: {every: 1m}", "updates: {auto: major}", "updates: {every: soon}"} {
		write(t, path, bad+"\nstacks: [{name: a, path: "+filepath.Join(dir, "a")+"}]\n")
		if _, err := Load(path); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
	write(t, path, "stacks: [{name: a, path: "+filepath.Join(dir, "a")+"}]\n")
	cfg, _ = Load(path)
	if cfg.Updates.Interval().Hours() != 6 || cfg.Updates.Auto != "off" {
		t.Errorf("defaults: %v %q", cfg.Updates.Interval(), cfg.Updates.Auto)
	}
}

func TestAddStack(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "main", "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "romm", "compose.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "other", "stack.yml"), "services: {}\n")
	cfgPath := filepath.Join(dir, "hoist.yaml")
	original := `# Hoist's config
git:
  name: Hoist
  email: hoist@localhost

stacks:
  # the big one
  - name: main-stack
    path: ` + filepath.Join(dir, "main") + `
    project: main-server # not the folder name
`
	write(t, cfgPath, original)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := cfg.AddStack(Stack{Name: "romm", Path: filepath.Join(dir, "romm")})
	if err != nil {
		t.Fatal(err)
	}
	if st.File != "compose.yml" || st.Project != "romm" {
		t.Errorf("added = %+v", st)
	}
	if _, err := cfg.AddStack(Stack{Name: "other", Path: filepath.Join(dir, "other"), File: "stack.yml", Project: "other"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfgPath)
	want := original + `  - name: romm
    path: ` + filepath.Join(dir, "romm") + `
    project: romm
  - name: other
    path: ` + filepath.Join(dir, "other") + `
    file: stack.yml
    project: other
`
	if string(data) != want {
		t.Errorf("hoist.yaml =\n%s\nwant\n%s", data, want)
	}
	again, err := Load(cfgPath)
	if err != nil || len(again.List()) != 3 || len(cfg.List()) != 3 {
		t.Fatalf("reload: %v %+v", err, again)
	}

	for _, bad := range []Stack{
		{Name: "romm", Path: filepath.Join(dir, "other"), File: "stack.yml", Project: "x"},     // name taken
		{Name: "romm2", Path: filepath.Join(dir, "romm"), Project: "x"},                        // same file
		{Name: "romm3", Path: filepath.Join(dir, "other"), File: "stack.yml", Project: "romm"}, // project taken
		{Name: "Bad Name", Path: filepath.Join(dir, "other")},
		{Name: "nofile", Path: filepath.Join(dir, "missing")},
	} {
		if _, err := cfg.AddStack(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if data2, _ := os.ReadFile(cfgPath); string(data2) != want {
		t.Error("a refused stack changed hoist.yaml")
	}
}

func TestAddStackLayouts(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "compose.yml"), "services: {}\n")
	write(t, filepath.Join(dir, "my stack", "compose.yml"), "services: {}\n")
	for name, tc := range map[string]struct{ in, want string }{
		"stacks first, then comments and another key": {
			in: "stacks:\n- name: a\n  path: " + filepath.Join(dir, "a") + "\n\n# checks\nupdates:\n  every: 6h\n",
			want: "stacks:\n- name: a\n  path: " + filepath.Join(dir, "a") + "\n" +
				"- name: my-stack\n  path: " + filepath.Join(dir, "my stack") + "\n  project: mystack\n" +
				"\n# checks\nupdates:\n  every: 6h\n",
		},
		"no trailing newline": {
			in: "stacks:\n    -   name: a\n        path: " + filepath.Join(dir, "a"),
			want: "stacks:\n    -   name: a\n        path: " + filepath.Join(dir, "a") + "\n" +
				"    -   name: my-stack\n        path: " + filepath.Join(dir, "my stack") + "\n        project: mystack\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hoist.yaml")
			write(t, path, tc.in)
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.AddStack(Stack{Name: "my-stack", Path: filepath.Join(dir, "my stack")}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != tc.want {
				t.Errorf("got\n%s\nwant\n%s", data, tc.want)
			}
		})
	}
}

func TestPins(t *testing.T) {
	dir := t.TempDir()
	stack := filepath.Join(dir, "main")
	write(t, filepath.Join(stack, "compose.yml"), "services: {}\n")
	path := filepath.Join(dir, "hoist.yaml")
	write(t, path, "stacks:\n  - name: main\n    path: "+stack+"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rollback.Healthy() != 5*time.Minute || cfg.Rollback.Keep != 2 {
		t.Errorf("rollback defaults = %+v", cfg.Rollback)
	}
	st, _ := cfg.Stack("main")
	if f := st.Files(); len(f) != 1 || f[0] != filepath.Join(stack, "compose.yml") {
		t.Errorf("files = %v", f)
	}
	pinDir := filepath.Join(cfg.PinDir("main"), "job1")
	write(t, filepath.Join(pinDir, "override.yml"), "services: {}\n")
	write(t, filepath.Join(cfg.PinDir("main"), "job0", "override.yml"), "services: {}\n")
	pin := &Pin{Deploy: "job1", Override: filepath.Join(pinDir, "override.yml"), Base: filepath.Join(pinDir, "compose.yml")}
	if err := cfg.SetPin("main", pin); err != nil {
		t.Fatal(err)
	}
	cfg.PrunePins("main")
	if _, err := os.Stat(filepath.Join(cfg.PinDir("main"), "job0")); !os.IsNotExist(err) {
		t.Error("an old pin's files were kept")
	}
	// A restarted Hoist (or the helper) sees the pin.
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st, _ = again.Stack("main")
	if st.Pin == nil || st.Pin.Deploy != "job1" {
		t.Fatalf("pin = %+v", st.Pin)
	}
	if f := st.Files(); len(f) != 2 || f[0] != pin.Base || f[1] != pin.Override {
		t.Errorf("pinned files = %v", f)
	}
	if err := again.SetPin("main", nil); err != nil {
		t.Fatal(err)
	}
	again.PrunePins("main")
	if _, err := os.Stat(again.PinDir("main")); !os.IsNotExist(err) {
		t.Error("pin folder left behind")
	}
}
