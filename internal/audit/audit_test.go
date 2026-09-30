package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "hoist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestRecordAndFilter(t *testing.T) {
	ctx := context.Background()
	l := open(t)
	events := []Event{
		{Stack: "main", Action: ComposeSave, Trigger: "ui", Detail: "chore(main): bump foyer", Commit: "abc"},
		{Stack: "main", Action: Deploy, Trigger: "foyer", Services: []string{"foyer", "shelfloom"}, Job: "j1", Result: Running},
		{Stack: "kopia", Action: GitPush, Trigger: "api", Result: Failed, Error: "rejected"},
		{Stack: "main", Action: GitCommit, Trigger: "ui", Services: []string{"foyer_db"}},
	}
	for _, e := range events {
		if _, err := l.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	check := func(q Query, want ...string) {
		t.Helper()
		got, err := l.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var actions []string
		for _, e := range got {
			actions = append(actions, e.Action)
		}
		if len(actions) != len(want) {
			t.Fatalf("%+v: got %v, want %v", q, actions, want)
		}
		for i := range want {
			if actions[i] != want[i] {
				t.Fatalf("%+v: got %v, want %v", q, actions, want)
			}
		}
	}
	check(Query{}, GitCommit, GitPush, Deploy, ComposeSave)
	check(Query{Stack: "main"}, GitCommit, Deploy, ComposeSave)
	check(Query{Action: "git."}, GitCommit, GitPush)
	check(Query{Trigger: "foyer"}, Deploy)
	check(Query{Result: Failed}, GitPush)
	check(Query{Service: "foyer"}, Deploy) // not foyer_db
	check(Query{Service: "foyer_"})        // _ isn't a wildcard
	check(Query{Before: 3}, Deploy, ComposeSave)
	check(Query{Limit: 1}, GitCommit)
	check(Query{Since: time.Now().Add(time.Hour)})

	got, _ := l.List(ctx, Query{Trigger: "foyer"})
	if s := got[0].Services; len(s) != 2 || s[1] != "shelfloom" {
		t.Fatalf("services = %v", s)
	}
}

func TestFinishJob(t *testing.T) {
	ctx := context.Background()
	l := open(t)
	if _, err := l.Record(ctx, Event{Stack: "main", Action: Deploy, Trigger: "ui", Job: "j1", Result: Running, Detail: "whole stack"}); err != nil {
		t.Fatal(err)
	}
	if err := l.FinishJob(ctx, "j1", Failed, "", "up failed"); err != nil {
		t.Fatal(err)
	}
	got, _ := l.List(ctx, Query{})
	if e := got[0]; e.Result != Failed || e.Error != "up failed" || e.Detail != "whole stack" {
		t.Fatalf("event = %+v", e)
	}
	// A finished job isn't changed again.
	_ = l.FinishJob(ctx, "j1", OK, "Recreated foyer", "")
	got, _ = l.List(ctx, Query{})
	if got[0].Result != Failed {
		t.Fatalf("finished event was changed: %+v", got[0])
	}
}

func TestReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hoist.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Record(ctx, Event{Stack: "main", Action: EnvSave, Trigger: "ui"})
	l.Close()
	l, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got, _ := l.List(ctx, Query{})
	if len(got) != 1 || got[0].Action != EnvSave {
		t.Fatalf("got %+v", got)
	}
}
