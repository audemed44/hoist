package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

// setup makes a bare remote and two clones of it.
func setup(t *testing.T) (a, b string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	a, b = filepath.Join(root, "a"), filepath.Join(root, "b")
	git(t, root, "clone", "-q", remote, a)
	if err := os.WriteFile(filepath.Join(a, "compose.yml"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, a, "add", ".")
	git(t, a, "commit", "-q", "-m", "first")
	git(t, a, "push", "-q", "-u", "origin", "main")
	git(t, root, "clone", "-q", remote, b)
	return a, b
}

func TestCommitPushPull(t *testing.T) {
	ctx := context.Background()
	a, b := setup(t)
	ra, err := Open(ctx, filepath.Join(a))
	if err != nil || ra == nil {
		t.Fatalf("open: %v %v", ra, err)
	}
	file := filepath.Join(a, "compose.yml")

	if _, err := ra.Commit(ctx, file, "noop", Author{"Hoist", "h@h"}); !errors.Is(err, ErrNothingToCommit) {
		t.Errorf("commit without changes: %v", err)
	}
	_ = os.WriteFile(file, []byte("v2\n"), 0o644)
	// Unrelated work in progress must stay out of the commit.
	_ = os.WriteFile(filepath.Join(a, "other.txt"), []byte("wip\n"), 0o644)
	git(t, a, "add", "other.txt")

	st, _ := ra.Status(ctx, file, 0)
	if !st.Modified || st.Branch != "main" {
		t.Errorf("status before commit = %+v", st)
	}
	hash, err := ra.Commit(ctx, file, "update compose", Author{"Hoist", "h@h"})
	if err != nil || hash == "" {
		t.Fatalf("commit: %q %v", hash, err)
	}
	st, _ = ra.Status(ctx, file, 0)
	if st.Modified || st.Ahead != 1 || st.Head != hash {
		t.Errorf("status after commit = %+v (hash %s)", st, hash)
	}
	if err := ra.Push(ctx); err != nil {
		t.Fatal(err)
	}

	rb, _ := Open(ctx, b)
	fileB := filepath.Join(b, "compose.yml")
	if err := rb.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = rb.Status(ctx, fileB, 0)
	if st.Behind != 1 || st.FetchedAt == nil {
		t.Errorf("b status = %+v", st)
	}
	if err := rb.Pull(ctx); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(fileB)
	if string(data) != "v2\n" {
		t.Errorf("b has %q after pull", data)
	}

	log, err := rb.Log(ctx, fileB, 10)
	if err != nil || len(log) != 2 || log[0].Subject != "update compose" || log[0].Author != "Hoist" {
		t.Fatalf("log = %+v, %v", log, err)
	}
	old, err := rb.Show(ctx, log[1].Hash, log[1].Path)
	if err != nil || string(old) != "v1\n" {
		t.Errorf("show = %q, %v", old, err)
	}
}

func TestLogFollowsRenames(t *testing.T) {
	ctx := context.Background()
	a, _ := setup(t)
	if err := os.MkdirAll(filepath.Join(a, "main-stack"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, a, "mv", "compose.yml", "main-stack/compose.yml")
	git(t, a, "commit", "-q", "-m", "move")
	r, _ := Open(ctx, filepath.Join(a, "main-stack"))
	if r.Root != a {
		t.Errorf("root = %s", r.Root)
	}
	log, err := r.Log(ctx, filepath.Join(a, "main-stack", "compose.yml"), 10)
	if err != nil || len(log) != 2 {
		t.Fatalf("log = %+v, %v", log, err)
	}
	if log[0].Path != "main-stack/compose.yml" || log[1].Path != "compose.yml" {
		t.Errorf("paths = %q, %q", log[0].Path, log[1].Path)
	}
	if data, err := r.Show(ctx, log[1].Hash, log[1].Path); err != nil || string(data) != "v1\n" {
		t.Errorf("show old path = %q, %v", data, err)
	}
}

func TestOpenOutsideRepo(t *testing.T) {
	r, err := Open(context.Background(), t.TempDir())
	if r != nil || err != nil {
		t.Errorf("got %v, %v", r, err)
	}
}

func TestShowRejectsOptions(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	for _, c := range [][2]string{{"--output=x", "a"}, {"abcd", "../x"}, {"HEAD", "a"}} {
		if _, err := r.Show(context.Background(), c[0], c[1]); err == nil {
			t.Errorf("Show(%q, %q) should fail", c[0], c[1])
		}
	}
}

func TestRedact(t *testing.T) {
	in := "fatal: unable to access 'https://user:ghp_secret@github.com/a/b/': 403"
	want := "fatal: unable to access 'https://***@github.com/a/b/': 403"
	if got := redact(in); got != want {
		t.Errorf("redact = %q", got)
	}
}
