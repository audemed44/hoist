# Hoist

Instructions for coding agents working in this repository. `CLAUDE.md`
imports this file.

## Project

Deploys a homelab's docker compose stacks from a browser: edit the compose
file and `.env`, commit and push, pull and deploy. A Go server
(`cmd/hoist`, `internal/`) serves a JSON API and the Preact + TypeScript
frontend (`frontend/`), built into `web/dist` and embedded in the binary.
Config is `/config/hoist.yaml`; deploy records and logs are files in
`/config/jobs/`; the audit log is SQLite in `/config/hoist.db`
(`internal/audit`), written by both Hoist and its self-deploy helper.

## Constraints

- **Low memory is a feature.** The server idles around 4 MB RSS. Compose and
  git run as child processes only while they're needed. The only
  background work is the update check (`internal/updates`, every
  `updates.every`, default 6h): manifest HEADs and tag lists, never pulls.
  Git fetches happen on page loads, at most every two minutes.
- The Go module has two direct dependencies: yaml.v3 and modernc.org/sqlite
  (pure Go, so the build stays static and cgo-free; the audit log). Justify
  any new one. The audit database uses one connection that closes when
  idle, so it holds no page cache between writes.
- Hoist can do anything root can, so: every `/api/` call needs the token
  (bearer or the derived session cookie), and state-changing browser
  requests from another origin are refused (`sameOrigin`). Keep both for
  every new endpoint, and record anything that changes a stack with
  `s.record` (`internal/server/audit.go`). Compose runs with a minimal
  environment (`compose.environ`): shell variables override `.env` in
  interpolation, so anything extra there (the token, Hoist's TZ) leaks into
  or changes stacks.
- Stacks must keep their project name, folder and compose file, or compose
  won't recognise the running containers. The deploy plan compares
  `docker compose config --hash` with the containers'
  `com.docker.compose.config-hash` label; the compose version in the
  Dockerfile should match the user's host.
- Hoist deploying its own stack goes through a helper container
  (`docker.RunHelper`, `hoist job <id>`) that outlives it. Anything that
  deploys must go through `startDeploy`.
- `.env` values never appear in list responses or the audit log; they're
  fetched one at a time. Edits keep comments and key order (`internal/envfile`).
- UI style is Foyer's: Swiss editorial, always dark, heavy Inter headlines,
  tracked uppercase eyebrows, 2px rules over numbered headings, square
  corners, one accent (#2563ff). Check phone width too.

## Commits

Conventional Commits: `<type>(<scope>): <summary>`, e.g. `feat(deploy): ...`.

## Checks before pushing

```sh
go vet ./... && go test -race ./...        # needs web/dist (npm run build)
cd frontend && npm run format:check && npm run typecheck && npm test && npm run build
docker build -t hoist:dev .
```
