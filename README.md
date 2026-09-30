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
  before anything is written, a diff to review, then a commit with a
  Conventional Commits message written for you
  (`chore(main-stack): bump shelfloom 0.4 → 0.5`) and a push. The review
  points out host ports and container names the file shares with its own
  services, other stacks, or containers Hoist doesn't manage, and suggests
  fixes: pin third-party images left on `:latest` (images from your own
  GitHub account may follow it), add a restart policy and a healthcheck,
  and move secrets typed into the file to `.env`. **Add service** looks an
  image up in its registry (without pulling) and inserts a service with a
  pinned version, free host ports for what it exposes, and `./<service>/`
  folders for its volumes.
- **Environment**: edit the stack's `.env` (kept out of git). Values stay on
  the server until you reveal one; variables the compose file uses but
  aren't set, and ones it doesn't use, are pointed out.
- **History**: every version of the compose file, following renames. Open
  any of them in the editor to roll back.
- **Git**: ahead/behind the remote, pull (fast-forward only) and push.
- **Drift**: when the compose file is edited on the server, outside Hoist,
  the stack says so and shows the difference from the last commit, to
  commit (with a suggested message), discard, or take into the editor.
  Deploys made meanwhile are marked as including uncommitted changes.
- **Update checks**: every few hours Hoist asks the registries, without
  pulling, whether a tag now points to a newer image and whether a pinned
  version has newer releases. Apply one with a click (the tag bump is
  committed like any edit), or let a policy apply them for you.
- **Self-update**: Hoist can deploy its own stack. A short-lived helper
  container runs that deploy, so it finishes while Hoist is replaced.
- **Add stacks**: adopt a compose project already running on the server
  (its folder, compose file and project name are read from its
  containers, so nothing gets recreated), or create a new stack folder with
  a compose file, committed and optionally deployed. Either is appended to
  `hoist.yaml`, leaving the rest of the file as it was.
- **Activity**: an audit log of every deploy, edit, commit, `.env` change
  and update, with where it came from (browser, API token, Foyer or an
  update policy) and how it ended. Filter by stack, action, trigger and
  result. `.env` edits are recorded by key name only, never values.
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
  conventional: true     # require Conventional Commits messages (default)

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
  remote as configured in that repo: a token in an `https://` URL, or ssh
  with a deploy key (below).
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org),
  scoped to the stack: Hoist suggests `feat(main-stack): add romm` when a
  service is added and `chore(main-stack): bump shelfloom 0.4 → 0.5` for
  image bumps and other changes, and refuses messages that don't fit unless
  `conventional: false`.
- `.env` in the stack folder is what compose reads for `${VARS}`. Keep it out
  of git.

Then open Hoist and sign in with `HOIST_TOKEN`.

### Updates

```yaml
updates:
  every: 6h        # how often to check; "off" checks only when you ask
  auto: "off"      # the default policy (below)
  notify: http://apprise-api:8000/notify/hoist   # optional Apprise API URL

stacks:
  - name: main-stack
    path: /home/you/homelab/main-stack
    updates:
      auto: digest           # this stack's policy
      services:
        shelfloom: minor     # and one service's
```

- **What's checked**: for every service, whether its tag (e.g. `latest`)
  now points to a different image than the one running; and for version
  tags (`1.2.3`, `v1.2`, `5.0.1-ls300`), which newer versions exist with
  the same shape (an `-alpine` tag only moves to `-alpine` tags, release
  candidates are ignored). Images pinned by digest and ones built locally
  are skipped.
- **Policies**: `off` only reports. `digest` redeploys services whose tag
  got a new image. `patch` and `minor` also bump pinned versions that far:
  the tag is changed in the compose file, committed
  (`chore(main-stack): bump shelfloom 0.4.1 → 0.4.2`), pushed and deployed.
  Major versions are never applied automatically.
- A version is only bumped when the image is written out in the compose
  file on one line; a tag set through `${VAR}` is reported but left alone.
- With `notify` set, Hoist posts each automatic update's result, and any
  failure, to Apprise.

### Pushing with a deploy key

A deploy key can only write to the one repo, unlike a personal token:

```sh
mkdir -p config/ssh && ssh-keygen -t ed25519 -N '' -f config/ssh/id_ed25519
ssh-keyscan -t ed25519 github.com > config/ssh/known_hosts   # check it against GitHub's published fingerprint
gh repo deploy-key add config/ssh/id_ed25519.pub --repo you/stacks --allow-write
```

Set the repo's remote to `git@github.com:you/stacks.git`, and give Hoist:

```yaml
    environment:
      - GIT_SSH_COMMAND=ssh -i /config/ssh/id_ed25519 -o UserKnownHostsFile=/config/ssh/known_hosts -o IdentitiesOnly=yes
```

| Variable | Default | |
|---|---|---|
| `HOIST_TOKEN` | (required) | What you sign in with, and Foyer's key; anything but empty |
| `HOIST_READ_ONLY` | `false` | Show everything, change nothing |
| `HOIST_PORT` | `8080` | |
| `HOIST_CONFIG_DIR` | `/config` | `hoist.yaml`, the deploy logs (`jobs/`) and the audit log (`hoist.db`) |
| `HOIST_COMPOSE` | `docker-compose` | Compose binary |
| `HOIST_HOST_HOME` | `$HOME` | Your home folder on the host; compose expands `~/` in bind mounts with it |

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
  can't read the token through `${HOIST_TOKEN}`. (Nor Hoist's `TZ`: shell
  variables override `.env` in compose, so passing them on would change
  stacks.)

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
