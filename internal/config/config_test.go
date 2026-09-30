package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
