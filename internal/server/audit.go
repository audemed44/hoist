package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/envfile"
)

// triggerOf tells a signed-in browser (the session cookie) from a script
// using the token.
func triggerOf(r *http.Request) string {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return "api"
	}
	return "ui"
}

// record adds to the audit log. A failure there is logged, not returned: the
// action it describes has already happened.
func (s *Server) record(e audit.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.Audit.Record(ctx, e); err != nil {
		slog.Warn("audit: could not record", "action", e.Action, "stack", e.Stack, "err", err)
	}
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := audit.Query{
		Stack: q.Get("stack"), Action: q.Get("action"), Trigger: q.Get("trigger"),
		Result: q.Get("result"), Service: q.Get("service"),
	}
	query.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	query.Limit, _ = strconv.Atoi(q.Get("limit"))
	if since := q.Get("since"); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since: expected a time like 2026-09-30T00:00:00Z")
			return
		}
		query.Since = t
	}
	events, err := s.Audit.List(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// envChanges describes an edit of .env by key name only; values never go in
// the log.
func envChanges(before, after *envfile.File) string {
	var added, changed, removed []string
	old := map[string]bool{}
	for _, k := range before.Keys() {
		old[k] = true
	}
	seen := map[string]bool{}
	for _, k := range after.Keys() {
		if seen[k] {
			continue
		}
		seen[k] = true
		if !old[k] {
			added = append(added, k)
			continue
		}
		a, _ := after.Get(k)
		b, _ := before.Get(k)
		if a != b {
			changed = append(changed, k)
		}
	}
	for _, k := range before.Keys() {
		if !seen[k] {
			seen[k] = true
			removed = append(removed, k)
		}
	}
	var parts []string
	for _, p := range []struct {
		verb string
		keys []string
	}{{"added", added}, {"changed", changed}, {"removed", removed}} {
		if len(p.keys) > 0 {
			parts = append(parts, p.verb+" "+strings.Join(p.keys, ", "))
		}
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, "; ")
}
