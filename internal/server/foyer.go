package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/releases"
)

// Hoist serves a card in the Foyer widget format
// (https://github.com/audemed44/foyer/blob/main/docs/app-widgets.md): one
// row per stack with a Deploy action, after the release board's apps and
// pull requests that need something (Deploy, Merge & deploy) and its merge
// and deploys under way. Foyer calls it with the token as a bearer token,
// from its server, so browsers never see the token.

type foyerStat struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Unit    string `json:"unit,omitempty"`
	Caption string `json:"caption,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

type foyerAction struct {
	Label   string `json:"label"`
	URL     string `json:"url"`
	Confirm string `json:"confirm,omitempty"`
}

type foyerItem struct {
	Title    string       `json:"title"`
	Subtitle string       `json:"subtitle,omitempty"`
	Caption  string       `json:"caption,omitempty"`
	URL      string       `json:"url,omitempty"`
	Action   *foyerAction `json:"action,omitempty"`
}

type foyerWidget struct {
	Version     int         `json:"version"`
	Stats       []foyerStat `json:"stats"`
	ItemsTitle  string      `json:"items_title"`
	ItemsLayout string      `json:"items_layout"`
	Items       []foyerItem `json:"items"`
}

func (s *Server) foyerWidget(w http.ResponseWriter, r *http.Request) {
	stacks := s.Config.List()
	infos := make([]StackInfo, len(stacks))
	var wg sync.WaitGroup
	for i, st := range stacks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos[i] = s.stackInfo(r.Context(), st, false)
		}()
	}
	wg.Wait()

	var running, total, pending, available, asleep int
	var last *jobs.Job
	out := foyerWidget{Version: 1, ItemsTitle: "Stacks", ItemsLayout: "list", Items: []foyerItem{}}
	for _, info := range infos {
		running += info.Counts.Running
		asleep += info.Counts.Asleep
		total += info.Counts.Services
		pending += info.Counts.Pending
		available += info.Counts.Updates
		if info.Last != nil && (last == nil || info.Last.Started.After(last.Started)) {
			last = info.Last
		}
		item := foyerItem{
			Title:    info.Name,
			Subtitle: fmt.Sprintf("%d/%d running", info.Counts.Running, info.Counts.Services),
			URL:      "/stacks/" + info.Name,
		}
		if info.Counts.Asleep > 0 {
			item.Subtitle += fmt.Sprintf(" · %d asleep", info.Counts.Asleep)
		}
		if info.Counts.Pending > 0 {
			item.Subtitle += fmt.Sprintf(" · %d to deploy", info.Counts.Pending)
		}
		if n := info.Counts.Updates; n == 1 {
			item.Subtitle += " · 1 update"
		} else if n > 1 {
			item.Subtitle += fmt.Sprintf(" · %d updates", n)
		}
		switch {
		case info.Error != "":
			item.Caption = "compose error"
		case info.Active != nil:
			item.Caption = "deploying…"
		case info.Pin != nil:
			item.Caption = "rolled back " + ago(info.Pin.At) + " (pinned)"
		case info.Git != nil && info.Git.Modified:
			item.Caption = "uncommitted changes"
		case info.Last != nil && info.Last.State == jobs.Failed:
			item.Caption = "deploy failed " + ago(info.Last.Started)
		case info.Last != nil:
			item.Caption = "deployed " + ago(info.Last.Started)
		}
		if !s.ReadOnly {
			item.Action = &foyerAction{
				Label:   "Deploy",
				URL:     "/api/foyer/deploy/" + info.Name,
				Confirm: "Pull the latest images and deploy " + info.Name + "?",
			}
		}
		out.Items = append(out.Items, item)
	}
	runTone := "good"
	if running+asleep < total {
		runTone = "warn"
	}
	runCaption := "containers"
	if asleep > 0 {
		runCaption = fmt.Sprintf("containers · %d asleep", asleep)
	}
	out.Stats = []foyerStat{
		{Label: "Running", Value: strconv.Itoa(running), Unit: "/" + strconv.Itoa(total), Caption: runCaption, Tone: runTone},
		{Label: "To deploy", Value: strconv.Itoa(pending), Caption: "services changed", Tone: map[bool]string{true: "accent"}[pending > 0]},
		{Label: "Updates", Value: strconv.Itoa(available), Caption: "images and versions", Tone: map[bool]string{true: "accent"}[available > 0]},
	}
	if last != nil {
		tone := ""
		if last.State == jobs.Failed {
			tone = "bad"
		}
		out.Stats = append(out.Stats, foyerStat{Label: "Last deploy", Value: ago(last.Started), Caption: last.Stack, Tone: tone})
	}
	if stat, items := s.foyerReleases(r.Context()); stat != nil {
		out.Stats = append(out.Stats, *stat)
		out.Items = append(items, out.Items...)
		out.ItemsTitle = "Releases and stacks"
	}
	// Foyer shows up to 12.
	out.Items = out.Items[:min(12, len(out.Items))]
	writeJSON(w, http.StatusOK, out)
}

// foyerDeploy starts a whole-stack deploy and tells Foyer where to follow it.
func (s *Server) foyerDeploy(w http.ResponseWriter, r *http.Request) {
	st, ok := s.stack(w, r)
	if !ok {
		return
	}
	job, status, err := s.startDeploy(st, nil, "foyer", "")
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"message":    "Deploying " + st.Name + "…",
		"status_url": "/api/foyer/jobs/" + job.ID,
		"url":        "/jobs/" + job.ID,
	})
}

func (s *Server) foyerJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Jobs.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	msg := "Deploying " + job.Stack + "…"
	switch job.State {
	case jobs.Done:
		msg = job.Stack + ": " + job.Result.Summary()
	case jobs.Failed:
		msg = job.Stack + ": " + job.Error
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": string(job.State), "message": msg, "url": "/jobs/" + job.ID})
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// foyerReleases is the release board for Foyer's card: a stat summing it
// up, and an item with its one action for each app or pull request that
// needs something. The last board is used even when it's a little old
// (it's refreshed behind the scenes), so the card stays quick.
func (s *Server) foyerReleases(ctx context.Context) (*foyerStat, []foyerItem) {
	if s.Releases.GitHub == nil {
		return nil, nil
	}
	b := s.Releases.Cached()
	switch {
	case b == nil:
		b = s.Releases.Get(ctx, boardAge)
	case time.Since(b.CheckedAt) > boardAge:
		go s.Releases.Get(context.Background(), boardAge)
	}
	if b.Error != "" {
		return &foyerStat{Label: "Releases", Value: "!", Caption: "GitHub: " + b.Error, Tone: "bad"}, nil
	}
	sum := b.Summary()
	var parts []string
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	parts = append(parts, plural(sum.PRs, "PR open", "PRs open"))
	if sum.Ready > 0 {
		parts = append(parts, strconv.Itoa(sum.Ready)+" ready to deploy")
	}
	if sum.Failing > 0 {
		parts = append(parts, strconv.Itoa(sum.Failing)+" failing")
	}
	stat := &foyerStat{Label: "Releases", Value: strconv.Itoa(sum.Ready + sum.ToMerge), Caption: strings.Join(parts, " · ")}
	switch {
	case sum.Failing > 0:
		stat.Tone = "bad"
	case sum.Ready+sum.ToMerge > 0:
		stat.Tone = "accent"
	}

	var items []foyerItem
	ships := s.ships.list()
	for _, a := range b.Apps {
		name := a.Repo[strings.Index(a.Repo, "/")+1:]
		path := "/api/foyer/releases/" + a.Repo
		// A merged pull request leaves the board, so its merge and deploy
		// stands in for it, under the same title (Foyer follows the action
		// by it).
		shipped := map[int]bool{}
		for _, sh := range shipsOf(ships, a) {
			shipped[sh.Number] = true
			item := foyerItem{Title: name + " #" + strconv.Itoa(sh.Number), Subtitle: sh.Title, URL: "/releases"}
			switch sh.State {
			case ShipBuilding:
				item.Caption = "building image…"
			case ShipDeploying:
				item.Caption = "deploying…"
			case ShipDone:
				item.Caption = "deployed " + ago(*sh.Finished)
			case ShipFailed:
				item.Caption = "merge and deploy failed"
			default:
				continue
			}
			items = append(items, item)
		}
		pinned := false
		if st, ok := s.Config.Stack(a.Stack); ok && st.Pin != nil {
			pinned = true
		}
		switch a.State {
		case releases.Ready:
			item := foyerItem{Title: name, Subtitle: "image ready, not deployed", URL: "/releases"}
			if a.Behind > 0 {
				item.Caption = plural(a.Behind, "new commit", "new commits")
			}
			if pinned {
				item.Caption = a.Stack + " is rolled back"
			} else if !s.ReadOnly {
				item.Action = &foyerAction{Label: "Deploy", URL: path + "/deploy",
					Confirm: "Pull and deploy " + name + " (" + strings.Join(a.Services, ", ") + ")?"}
			}
			items = append(items, item)
		case releases.BuildFailed:
			items = append(items, foyerItem{Title: name, Subtitle: "image build failed", URL: "/releases"})
		}
		for _, p := range a.PRs {
			if shipped[p.Number] {
				continue // a board from before the merge
			}
			item := foyerItem{Title: name + " #" + strconv.Itoa(p.Number), Subtitle: p.Title, URL: "/releases"}
			switch p.State {
			case releases.PRReady:
				item.Caption = "ready to merge"
				switch {
				case s.ReadOnly:
				case pinned:
					// A rolled-back stack doesn't take new images, so this
					// only merges.
					item.Action = &foyerAction{Label: "Merge", URL: path + "/pulls/" + strconv.Itoa(p.Number) + "/merge",
						Confirm: "Rebase and merge #" + strconv.Itoa(p.Number) + " into " + a.Branch + ", and delete " + p.Branch + "? " + a.Stack + " is rolled back, so it won't be deployed."}
				default:
					item.Action = &foyerAction{Label: "Merge & deploy", URL: path + "/pulls/" + strconv.Itoa(p.Number) + "/ship",
						Confirm: "Rebase and merge #" + strconv.Itoa(p.Number) + " into " + a.Branch + ", wait for its image to build, then deploy " + name + " (" + strings.Join(a.Services, ", ") + ")?"}
				}
			case releases.PRFailing:
				item.Caption = "CI failing"
			default:
				continue
			}
			items = append(items, item)
		}
	}
	return stat, items
}

// foyerReleaseApp finds the app of a Foyer action.
func (s *Server) foyerReleaseApp(w http.ResponseWriter, r *http.Request) (*releases.App, bool) {
	a, status, err := s.app(r, releaseTarget{Repo: r.PathValue("owner") + "/" + r.PathValue("repo")})
	if err != nil {
		writeError(w, status, err.Error())
		return nil, false
	}
	return a, true
}

func (s *Server) foyerReleaseDeploy(w http.ResponseWriter, r *http.Request) {
	a, ok := s.foyerReleaseApp(w, r)
	if !ok {
		return
	}
	job, status, err := s.deployApp(a, "foyer")
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"message":    "Deploying " + a.Repo + "…",
		"status_url": "/api/foyer/jobs/" + job.ID,
		"url":        "/jobs/" + job.ID,
	})
}

func (s *Server) foyerReleaseMerge(w http.ResponseWriter, r *http.Request) {
	a, ok := s.foyerReleaseApp(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad pull request number")
		return
	}
	res, status, err := s.merge(r.Context(), a, n, false, "foyer")
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	msg := fmt.Sprintf("Merged %s#%d as %s; the image build starts on GitHub", a.Repo, n, shortSHA(res.SHA))
	writeJSON(w, http.StatusOK, map[string]string{"message": msg, "url": "/releases"})
}

// foyerReleaseShip merges a pull request, waits for its image and deploys
// it; Foyer follows it at its status URL.
func (s *Server) foyerReleaseShip(w http.ResponseWriter, r *http.Request) {
	a, ok := s.foyerReleaseApp(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad pull request number")
		return
	}
	sh, status, err := s.ship(r.Context(), a, n, false, "foyer")
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"message":    fmt.Sprintf("Merged %s#%d as %s; waiting for the image build", a.Repo, n, shortSHA(sh.SHA)),
		"status_url": "/api/foyer/ships/" + sh.ID,
		"url":        "/releases",
	})
}

// foyerShip is a merge and deploy's state in Foyer's terms.
func (s *Server) foyerShip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var sh *Ship
	for _, x := range s.ships.list() {
		if x.ID == id {
			sh = &x
		}
	}
	if sh == nil {
		writeError(w, http.StatusNotFound, "no such merge and deploy")
		return
	}
	state := "running"
	switch sh.State {
	case ShipDone:
		state = "done"
	case ShipFailed, ShipCancelled:
		state = "failed"
	}
	url := "/releases"
	if sh.Job != "" {
		url = "/jobs/" + sh.Job
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"state":   state,
		"message": fmt.Sprintf("%s#%d: %s", sh.Repo, sh.Number, sh.Message),
		"url":     url,
	})
}
