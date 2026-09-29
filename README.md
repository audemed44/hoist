# Hoist

Deploy your homelab's compose stacks from a browser. Edit the compose file
and its `.env`, commit and push to git, and pull and deploy, in a Go binary
that idles at about 4 MB of RAM. A lightweight replacement for
[Komodo](https://komo.do)'s stack management.

- **Stacks**: every service with its container, and what a deploy would
  change (create, recreate, start, remove) before you press the button.
- **Deploy**: `docker compose pull`, then `up -d --remove-orphans`, with a
  live log. Only services whose config or image changed are recreated, and
  the result says which. Deploy a whole stack or a single service.
- **Compose editor**: YAML editor, checked with `docker compose config`
  before anything is written, a diff to review, then a commit with a message
  written for you (`main-stack: shelfloom 0.4 → 0.5`) and a push.
- **Environment**: edit the stack's `.env` (kept out of git). Values stay on
  the server until you reveal one; variables the compose file uses but
  aren't set, and ones it doesn't use, are pointed out.
- **History**: every version of the compose file, following renames. Open
  any of them in the editor to roll back.
- **Git**: ahead/behind the remote, pull (fast-forward only), push, and
  commit edits made outside Hoist.
- **Self-update**: Hoist can deploy its own stack. A short-lived helper
  container runs that deploy, so it finishes while Hoist is replaced.
- **Foyer**: serves a card in the
  [Foyer widget format](https://github.com/audemed44/foyer/blob/main/docs/app-widgets.md)
  with a Deploy button per stack.

## Install

Hoist runs in its own stack, next to the stacks it manages. See
[docker-compose.example.yml](docker-compose.example.yml), then write
`config/hoist.yaml`:

```yaml
git:
  name: Hoist            # commit author
  email: hoist@example.com
  push: true             # push after each commit (default)

stacks:
  - name: main-stack
    path: /home/you/homelab/main-stack   # same path as on the host
    project: main-server                 # must match the running containers
    # file: docker-compose.yml           # found automatically
    # remove_orphans: true               # default
  - name: kopia
    path: /home/you/homelab/kopia
```

- `path` must be the stack folder's path **on the host**, and Hoist must see
  it at that same path. Compose hands bind-mount paths straight to Docker.
- `project` is the compose project name. It defaults to the folder name, as
  in compose; set it when your containers were started under another name
  (`docker inspect <container> --format '{{index .Config.Labels "com.docker.compose.project"}}'`).
  With the wrong name compose treats the running containers as someone else's.
- Git is optional per stack: if the folder is in a git work tree, edits are
  committed there (the repo can hold several stacks). Pushing uses the
  remote as configured in that repo, e.g. a token in an `https://` URL.
- `.env` in the stack folder is what compose reads for `${VARS}`. Keep it out
  of git.

Then open Hoist and sign in with `HOIST_TOKEN`.

| Variable | Default | |
|---|---|---|
| `HOIST_TOKEN` | (required) | API token, at least 16 characters |
| `HOIST_READ_ONLY` | `false` | Show everything, change nothing |
| `HOIST_PORT` | `8080` | |
| `HOIST_CONFIG_DIR` | `/config` | `hoist.yaml` and the deploy logs (`jobs/`) |
| `HOIST_COMPOSE` | `docker-compose` | Compose binary |

**Keep compose in step with the host.** The image ships docker compose
5.5.1. Compose decides whether to recreate a container by comparing a hash
of its config, and that hash can change between compose versions. If the
host's version differs a lot, the first deploy may recreate everything once.
Hoist's plan shows it before you deploy: if every service shows as
"recreate" with no edits, check `docker compose version`.

## Security

Anything that can deploy through Hoist can run any container, which means
root on the host. So:

- Every API call needs the token: as a bearer token (Foyer, scripts), or the
  session cookie a browser gets by entering it once. The cookie is derived
  from the token, so changing the token signs every browser out.
- Browsers can't make changes from another origin, including other
  subdomains of your domain.
- The compose commands don't see Hoist's own environment, so a compose file
  can't read the token through `${HOIST_TOKEN}`.

## Foyer

Add Hoist to Foyer as an `app` widget:

```yaml
      - name: Hoist
        url: https://hoist.example.com
        widget:
          type: app
          url: http://host.docker.internal:9130/api/foyer/widget
          key: ${HOIST_TOKEN}
```

It shows containers running, services waiting for a deploy and the last
deploy, with a row per stack. Each row's **Deploy** runs
`POST /api/foyer/deploy/<stack>` and follows `/api/foyer/jobs/<id>`.

## Coming from Komodo

See [docs/migrating-from-komodo.md](docs/migrating-from-komodo.md).

## Development

```sh
cd frontend && npm ci && npm run build    # into web/dist, embedded in the binary
go test ./...
HOIST_TOKEN=devdevdevdevdevdev HOIST_CONFIG_DIR=./dev HOIST_COMPOSE="docker compose" go run ./cmd/hoist
cd frontend && npm run dev                # UI on :5173, API proxied to :8080 (or HOIST_URL)
```
