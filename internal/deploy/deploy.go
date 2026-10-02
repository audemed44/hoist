// Package deploy runs a stack's deploy: pull the images, `up -d`, and work
// out from the containers before and after what actually changed.
package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/jobs"
)

// Timeout bounds a whole deploy; pulls of big images can be slow.
const Timeout = 30 * time.Minute

// Run deploys j's stack (or just j.Services), logging to the job's log file,
// and records the result.
func Run(ctx context.Context, dock *docker.Client, store *jobs.Store, stack config.Stack, j *jobs.Job) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	log, err := os.OpenFile(store.LogPath(j.ID), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		_ = store.Finish(j, nil, err)
		return err
	}
	defer log.Close()
	res, err := run(ctx, dock, stack, j.Services, j.Asleep, log)
	if err != nil {
		fmt.Fprintf(log, "\n✗ %v\n", err)
	} else {
		fmt.Fprintf(log, "\n✓ %s\n", res.Summary())
	}
	if ferr := store.Finish(j, res, err); ferr != nil && err == nil {
		err = ferr
	}
	return err
}

func run(ctx context.Context, dock *docker.Client, stack config.Stack, services, asleep []string, log io.Writer) (*jobs.Result, error) {
	before, err := dock.Project(ctx, stack.Project)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	pull := append([]string{"pull", "--ignore-buildable"}, services...)
	fmt.Fprintf(log, "$ docker compose %s\n", join(pull))
	if err := compose.Run(ctx, stack, log, pull...); err != nil {
		return nil, fmt.Errorf("pull failed: %w", err)
	}
	up := []string{"up", "--detach"}
	if stack.ShouldRemoveOrphans() {
		up = append(up, "--remove-orphans")
	}
	up = append(up, services...)
	fmt.Fprintf(log, "\n$ docker compose %s\n", join(up))
	upErr := compose.Run(ctx, stack, log, up...)
	// Report what changed even when up failed halfway.
	after, err := dock.Project(context.WithoutCancel(ctx), stack.Project)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	res := Diff(before, after)
	if upErr != nil {
		return res, fmt.Errorf("up failed: %w", upErr)
	}
	if back := backToSleep(res, before, asleep); len(back) > 0 {
		stop := append([]string{"stop"}, back...)
		fmt.Fprintf(log, "\n# Gatehouse had put these to sleep; the deploy only started them.\n$ docker compose %s\n", join(stop))
		if err := compose.Run(ctx, stack, log, stop...); err != nil {
			fmt.Fprintf(log, "could not put them back to sleep: %v\n", err)
		} else {
			res.Started = slices.DeleteFunc(res.Started, func(s string) bool { return slices.Contains(back, s) })
		}
	}
	return res, nil
}

// backToSleep lists the services the deploy merely started whose
// containers Gatehouse had put to sleep. Recreated ones stay up: they run
// something new, and Gatehouse stops them again once they're idle.
func backToSleep(res *jobs.Result, before []docker.Container, asleep []string) []string {
	var out []string
	for _, c := range before {
		if slices.Contains(asleep, c.Name) && slices.Contains(res.Started, c.Service) && !slices.Contains(out, c.Service) {
			out = append(out, c.Service)
		}
	}
	sort.Strings(out)
	return out
}

func join(args []string) string { return strings.Join(args, " ") }

// Diff compares a project's containers before and after a deploy.
func Diff(before, after []docker.Container) *jobs.Result {
	index := func(cs []docker.Container) map[string]docker.Container {
		m := map[string]docker.Container{}
		for _, c := range cs {
			if !c.OneOff {
				m[c.Name] = c
			}
		}
		return m
	}
	b, a := index(before), index(after)
	res := &jobs.Result{Created: []string{}, Recreated: []string{}, Removed: []string{}, Started: []string{}, Updated: []string{}}
	// Replicas of a scaled service share its name in the result.
	add := func(list *[]string, service string) {
		if !slices.Contains(*list, service) {
			*list = append(*list, service)
		}
	}
	for name, c := range a {
		old, ok := b[name]
		switch {
		case !ok:
			add(&res.Created, c.Service)
		case old.ID != c.ID:
			add(&res.Recreated, c.Service)
			if old.ImageID != c.ImageID {
				add(&res.Updated, c.Service)
			}
		case old.State != "running" && c.State == "running":
			add(&res.Started, c.Service)
		}
	}
	for name, c := range b {
		if _, ok := a[name]; !ok {
			add(&res.Removed, c.Service)
		}
	}
	for _, list := range []*[]string{&res.Created, &res.Recreated, &res.Removed, &res.Started, &res.Updated} {
		sort.Strings(*list)
	}
	return res
}
