package server

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/audemed44/hoist/internal/jobs"
)

// Hoist serves a card in the Foyer widget format
// (https://github.com/audemed44/foyer/blob/main/docs/app-widgets.md): one
// row per stack with a Deploy action. Foyer calls it with the token as a
// bearer token, from its server, so browsers never see the token.

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
	infos := make([]StackInfo, len(s.Config.Stacks))
	var wg sync.WaitGroup
	for i, st := range s.Config.Stacks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos[i] = s.stackInfo(r.Context(), st, false)
		}()
	}
	wg.Wait()

	var running, total, pending, available int
	var last *jobs.Job
	out := foyerWidget{Version: 1, ItemsTitle: "Stacks", ItemsLayout: "list", Items: []foyerItem{}}
	for _, info := range infos {
		running += info.Counts.Running
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
	if running < total {
		runTone = "warn"
	}
	out.Stats = []foyerStat{
		{Label: "Running", Value: strconv.Itoa(running), Unit: "/" + strconv.Itoa(total), Caption: "containers", Tone: runTone},
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
