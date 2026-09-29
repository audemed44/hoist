package jobs

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestLifecycle(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(Job{Stack: "main", Trigger: "ui"})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(a.ID) || a.State != Running {
		t.Errorf("job = %+v", a)
	}
	if _, err := os.Stat(s.LogPath(a.ID)); err != nil {
		t.Errorf("no log file: %v", err)
	}
	if s.Active("main") == nil || s.Active("kopia") != nil || s.Latest("main") != nil {
		t.Error("active/latest wrong while running")
	}
	if err := s.Finish(a, &Result{Recreated: []string{"web"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(a.ID)
	if got.State != Done || got.Finished == nil || got.Result.Summary() != "Recreated web" {
		t.Errorf("finished job = %+v", got)
	}
	if s.Active("main") != nil || s.Latest("main").ID != a.ID {
		t.Error("active/latest wrong after finishing")
	}

	time.Sleep(1100 * time.Millisecond) // IDs sort by second
	b, _ := s.Create(Job{Stack: "main"})
	_ = s.Finish(b, nil, errors.New("pull failed"))
	list := s.List("main", 10)
	if len(list) != 2 || list[0].ID != b.ID || list[0].State != Failed || list[0].Error != "pull failed" {
		t.Errorf("list = %+v", list)
	}
	if _, err := s.Get("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get with a bad id: %v", err)
	}
}

func TestRecover(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	plain, _ := s.Create(Job{Stack: "main"})
	alive, _ := s.Create(Job{Stack: "hoist", Self: true})
	dead, _ := s.Create(Job{Stack: "other", Self: true})
	s.Recover(func(j *Job) bool { return j.ID == alive.ID })
	for id, want := range map[string]State{plain.ID: Failed, alive.ID: Running, dead.ID: Failed} {
		if j, _ := s.Get(id); j.State != want {
			t.Errorf("job %s: state %s, want %s", id, j.State, want)
		}
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	for i := range keep + 5 {
		j := &Job{ID: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format("20060102-150405") + "-abcdef", Stack: "main"}
		_ = s.write(j)
	}
	_, _ = s.Create(Job{Stack: "main"})
	if n := len(s.ids()); n != keep {
		t.Errorf("%d jobs kept, want %d", n, keep)
	}
	if _, err := s.Get("20260101-000000-abcdef"); !errors.Is(err, ErrNotFound) {
		t.Error("the oldest job should be gone")
	}
}
