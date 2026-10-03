package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/releases"
)

// RunReleases checks the release board every releases.every, when that's
// set, and notifies about what changed since the last check. The first
// check only notes how things stand.
func (s *Server) RunReleases(ctx context.Context) {
	every := s.Config.Releases.Interval()
	if every == 0 || s.Releases.GitHub == nil {
		return
	}
	var seen map[string]bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		b := s.Releases.Get(ctx, every/2)
		if b.Error != "" {
			continue
		}
		now := boardEvents(b)
		for key, e := range now {
			if seen != nil && !seen[key] {
				s.notify(s.Config.Releases.Notify, e.kind, e.title, e.body)
			}
		}
		seen = map[string]bool{}
		for key := range now {
			seen[key] = true
		}
	}
}

type boardEvent struct{ kind, title, body string }

// boardEvents are the things on the board worth a notification, keyed so
// each is told once: failing checks on a PR's commit, a failed build, an
// image waiting to be deployed.
func boardEvents(b *releases.Board) map[string]boardEvent {
	out := map[string]boardEvent{}
	for _, a := range b.Apps {
		name := a.Repo[strings.Index(a.Repo, "/")+1:]
		switch a.State {
		case releases.Ready:
			body := "A new image is published and waiting to be deployed"
			if len(a.Commits) > 0 {
				body += ": " + a.Commits[0].Message
				if a.Behind > 1 {
					body += fmt.Sprintf(" (and %d more)", a.Behind-1)
				}
			}
			out["ready "+a.ID+" "+a.Head+" "+a.Latest] = boardEvent{"info", "Hoist: " + name + " is ready to deploy", body}
		case releases.BuildFailed:
			if a.Build != nil {
				out[fmt.Sprintf("build %s %d", a.ID, a.Build.ID)] = boardEvent{"failure", "Hoist: " + name + "'s image build failed", a.Build.URL}
			}
		}
		for _, p := range a.PRs {
			if p.State == releases.PRFailing {
				out[fmt.Sprintf("ci %s %d %s", a.Repo, p.Number, p.SHA)] = boardEvent{"failure",
					fmt.Sprintf("Hoist: CI failing on %s#%d", name, p.Number), p.Title + "\n" + p.URL}
			}
		}
	}
	return out
}
