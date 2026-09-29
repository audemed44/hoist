# Moving from Komodo

Hoist picks up stacks Komodo deployed without restarting anything, as long as
it uses the same folder, compose file and project name.

## 1. Run Hoist next to Komodo, read-only

Start Hoist with `HOIST_READ_ONLY=true` and list your stacks in `hoist.yaml`
with the project names the containers already have:

```sh
docker inspect <any container of the stack> \
  --format '{{index .Config.Labels "com.docker.compose.project"}} {{index .Config.Labels "com.docker.compose.project.working_dir"}}'
```

Open each stack. Every service should say **up to date**. If services show
"recreate" without any edits, the project name, folder or compose version
doesn't match yet; fix that before going further.

## 2. Things Komodo did that now live elsewhere

- **Variables**: Komodo writes the stack's Environment into `.env` in the
  stack folder on every deploy. That file is already there, and Hoist edits
  it directly. If you used Komodo's `[[VARIABLE]]` interpolation, replace
  those with plain values or `${VAR}` first.
- **Extra args**: Hoist always runs `up -d --remove-orphans` (turn orphan
  removal off per stack with `remove_orphans: false`).
- **Git**: Hoist commits in whatever repo holds the stack folder and pushes
  to its configured remote. A token in the remote URL keeps working.
- **Registry logins**: not handled; public images need none. For private
  ones, run `docker login` with a config directory mounted into Hoist and
  set `DOCKER_CONFIG`.

## 3. Switch over

1. Stop Komodo Core (and Mongo). From now on only Hoist deploys; two
   tools rewriting `.env` and the compose file would trample each other.
2. Remove `HOIST_READ_ONLY` and restart Hoist.
3. Deploy each stack once from Hoist. Every service should report
   **Nothing changed**.
4. Remove Komodo: its stack, Mongo's volume when you're sure, and the
   Periphery agent (`systemctl --user disable --now periphery` if it runs as
   a user unit).
