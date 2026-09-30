// Command hoist serves the web UI and API. `hoist job <id>` runs one deploy
// and exits; Hoist starts it in a helper container to redeploy itself.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // the runtime image may have no zoneinfo; TZ needs this

	"github.com/audemed44/hoist/internal/audit"
	"github.com/audemed44/hoist/internal/compose"
	"github.com/audemed44/hoist/internal/config"
	"github.com/audemed44/hoist/internal/deploy"
	"github.com/audemed44/hoist/internal/docker"
	"github.com/audemed44/hoist/internal/jobs"
	"github.com/audemed44/hoist/internal/server"
	"github.com/audemed44/hoist/internal/updates"
	"github.com/audemed44/hoist/web"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	level := slog.LevelInfo
	if os.Getenv("HOIST_DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	compose.Bin = env("HOIST_COMPOSE", compose.Bin)

	configDir := env("HOIST_CONFIG_DIR", "/config")
	cfg, err := config.Load(filepath.Join(configDir, "hoist.yaml"))
	if err != nil {
		slog.Error("could not load the config", "err", err)
		os.Exit(1)
	}
	store, err := jobs.NewStore(filepath.Join(configDir, "jobs"))
	if err != nil {
		slog.Error("could not open the jobs folder", "err", err)
		os.Exit(1)
	}
	auditLog, err := audit.Open(filepath.Join(configDir, "hoist.db"))
	if err != nil {
		slog.Error("could not open the audit log", "err", err)
		os.Exit(1)
	}
	defer auditLog.Close()
	store.OnFinish = auditLog.JobFinished
	dock := docker.New(env("HOIST_DOCKER_SOCKET", "/var/run/docker.sock"))

	if len(os.Args) > 2 && os.Args[1] == "job" {
		code := runJob(cfg, dock, store, os.Args[2])
		auditLog.Close()
		os.Exit(code)
	}

	// Any token will do; it's yours to pick. Only an empty one is refused,
	// since that would leave the API open.
	token := os.Getenv("HOIST_TOKEN")
	if token == "" {
		slog.Error("set HOIST_TOKEN: it's what you sign in with, and what Foyer uses to deploy")
		os.Exit(1)
	}
	store.Recover(server.HelperAlive(dock))
	warnNoUser()

	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err)
	}
	readOnly := os.Getenv("HOIST_READ_ONLY") == "true" || os.Getenv("HOIST_READ_ONLY") == "1"
	checker := updates.New(cfg, dock, filepath.Join(configDir, "updates.json"))
	app := server.New(server.Options{Config: cfg, Docker: dock, Jobs: store, Audit: auditLog, Updates: checker, Token: token, ReadOnly: readOnly, Web: dist})
	srv := &http.Server{
		Addr:              ":" + env("HOIST_PORT", "8080"),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go app.RunUpdates(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("hoist listening", "addr", srv.Addr, "stacks", len(cfg.Stacks), "read_only", readOnly)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// runJob deploys a job recorded by the server; it's the helper container's
// entry point.
func runJob(cfg *config.Config, dock *docker.Client, store *jobs.Store, id string) int {
	job, err := store.Get(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, ok := cfg.Stack(job.Stack)
	if !ok {
		_ = store.Finish(job, nil, fmt.Errorf("no stack %q in hoist.yaml", job.Stack))
		return 1
	}
	if job.State != jobs.Running {
		fmt.Fprintf(os.Stderr, "job %s already finished\n", id)
		return 1
	}
	// Give the Hoist that started this a moment to answer the request
	// before compose replaces its container.
	time.Sleep(2 * time.Second)
	if log, err := os.OpenFile(store.LogPath(job.ID), os.O_WRONLY|os.O_APPEND, 0); err == nil {
		fmt.Fprintf(log, "Running in a helper container, so Hoist can be replaced while this carries on.\n\n")
		log.Close()
	}
	if err := deploy.Run(context.Background(), dock, store, st, job); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// warnNoUser explains the one thing that breaks when Hoist runs as a uid the
// image doesn't know: ssh (git over ssh) refuses to start.
func warnNoUser() {
	uid := os.Getuid()
	if _, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return
	}
	slog.Warn("no passwd entry for this uid, so git over ssh won't work; run as 1000 or mount /etc/passwd:/etc/passwd:ro",
		"uid", uid)
}

func healthcheck() int {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + env("HOIST_PORT", "8080") + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return 1
	}
	return 0
}
