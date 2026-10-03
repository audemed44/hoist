import { ExternalLink, RefreshCw } from "lucide-preact";
import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, appStateLabel, prStateLabel, runLabel, type Tone } from "../lib";
import type { PullRequest, ReleaseApp, ReleasesInfo } from "../types";
import { AppButtons, PRButtons, ShipNote, useReleaseActions } from "./ReleaseActions";
import { Dot, ErrorNote, SectionHead } from "./ui";

/**
 * Your own apps, from pull request to running container: open PRs and
 * their checks, what the branch has that the running image doesn't, the
 * image build, and whether a new image is waiting to be deployed.
 */
export function ReleasesPage(props: { readOnly: boolean }) {
  const { data, error, setData } = useData(() => api.releases(), 30_000);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshError, setRefreshError] = useState("");

  const refresh = async () => {
    setRefreshing(true);
    setRefreshError("");
    try {
      setData(await api.releases(true));
    } catch (e) {
      setRefreshError((e as Error).message);
    } finally {
      setRefreshing(false);
    }
  };

  if (!data) return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton page-skeleton" />;
  if (!data.configured) return <NotConfigured />;
  const s = data.summary;
  return (
    <div class="page">
      <header class="page-head">
        <div class="eyebrow eyebrow-accent">
          {data.apps.length} {data.apps.length === 1 ? "app" : "apps"} · {data.owners.join(", ")}
        </div>
        <h1 class="page-title">Releases</h1>
        <div class="figures">
          <Figure value={s.prs} label="Open PRs" tone={s.to_merge ? "accent" : ""} />
          <Figure value={s.to_merge} label="Ready to merge" tone={s.to_merge ? "good" : ""} />
          <Figure value={s.ready} label="Ready to deploy" tone={s.ready ? "accent" : ""} />
          <Figure value={s.failing} label="Failing" tone={s.failing ? "bad" : ""} />
        </div>
        <div class="updates-bar">
          <span>Checked GitHub {ago(data.checked_at)}</span>
          <span class="muted">when this page is open · image builds from {data.workflow}</span>
          <span class="spacer" />
          {refreshError && <span class="form-error">{refreshError}</span>}
          <button class="btn btn-ghost btn-small" onClick={refresh} disabled={refreshing}>
            <RefreshCw size={13} class={refreshing ? "spin" : ""} />
            {refreshing ? "Checking…" : "Check now"}
          </button>
        </div>
      </header>
      {data.error && <ErrorNote>{data.error}</ErrorNote>}
      {!data.apps.length && !data.error && (
        <div class="empty">
          None of the containers Hoist deploys says it was built from a repository of{" "}
          {data.owners.join(", ")}. Hoist reads the images' org.opencontainers.image.source label.
        </div>
      )}
      <div class="stack-list stagger">
        {data.apps.map((a, i) => (
          <AppCard
            key={a.id}
            index={i + 1}
            app={a}
            board={data}
            readOnly={props.readOnly}
            onChange={refresh}
          />
        ))}
      </div>
    </div>
  );
}

function NotConfigured() {
  return (
    <div class="page">
      <header class="page-head">
        <h1 class="page-title">Releases</h1>
      </header>
      <div class="note">
        <p>
          The release board reads your apps' pull requests, checks and image builds from GitHub.
          Give Hoist a fine-grained token in <code>HOIST_GITHUB_TOKEN</code>, limited to your
          repositories, with these permissions:
        </p>
        <ul class="perm-list">
          <li>
            <strong>Pull requests</strong>: read and write (list, merge)
          </li>
          <li>
            <strong>Contents</strong>: read and write (commits, rebase merge, delete the branch)
          </li>
          <li>
            <strong>Actions</strong>: read and write (CI status, re-run failed jobs)
          </li>
          <li>
            <strong>Metadata</strong>: read (always on)
          </li>
        </ul>
      </div>
    </div>
  );
}

function Figure(props: { value: number; label: string; tone: string }) {
  return (
    <div class={`figure ${props.tone}`}>
      <div class="figure-value">{props.value}</div>
      <div class="eyebrow figure-label">{props.label}</div>
    </div>
  );
}

const APP_TONE: Record<string, Tone> = {
  "build-failed": "bad",
  ready: "accent",
  building: "accent",
  "not-built": "",
  deployed: "good",
};

const PR_TONE: Record<string, Tone> = {
  ready: "good",
  "ci-failing": "bad",
  conflict: "bad",
  "ci-running": "accent",
  checking: "",
  draft: "",
  blocked: "warn",
  "changes-requested": "warn",
};

function short(sha: string | undefined): string {
  return sha ? sha.slice(0, 7) : "";
}

function AppCard(props: {
  index: number;
  app: ReleaseApp;
  board: ReleasesInfo;
  readOnly: boolean;
  onChange: () => void;
}) {
  const a = props.app;
  const [allCommits, setAllCommits] = useState(false);
  const actions = useReleaseActions(a, props.onChange);
  const name = a.repo.split("/")[1];
  const commits = allCommits ? a.commits : a.commits.slice(0, 4);
  return (
    <section class="stack-row release">
      <SectionHead index={props.index} title={name}>
        <span class={`chip release-state tone-chip-${APP_TONE[a.state]}`}>{appStateLabel(a)}</span>
      </SectionHead>
      <div class="stack-row-body">
        <dl class="facts">
          <div>
            <dt class="eyebrow">Repository</dt>
            <dd>
              <a class="link" href={a.url} target="_blank" rel="noreferrer">
                {a.repo} <ExternalLink size={11} />
              </a>
            </dd>
          </div>
          <div>
            <dt class="eyebrow">Running</dt>
            <dd class="mono">
              {a.running.revision ? (
                <a
                  class="link"
                  href={`${a.url}/commit/${a.running.revision}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  {short(a.running.revision)}
                </a>
              ) : (
                <span class="muted">no revision label</span>
              )}
              <span class="muted">
                {" "}
                · in{" "}
                <a class="link" href={`/stacks/${a.stack}`}>
                  {a.stack}
                </a>
                {a.pinned && " · rolled back"}
              </span>
            </dd>
          </div>
          <div>
            <dt class="eyebrow">{a.branch || "Branch"}</dt>
            <dd class="mono">
              {a.head ? short(a.head) : "—"}
              <span class={a.behind ? "tone-accent" : "muted"}>
                {" "}
                ·{" "}
                {a.behind
                  ? `${a.behind} ${a.behind === 1 ? "commit" : "commits"} ahead`
                  : a.head && a.head === a.running.revision
                    ? "running"
                    : ""}
              </span>
            </dd>
          </div>
          <div>
            <dt class="eyebrow">Image build</dt>
            <dd>
              {a.build ? (
                <a class="link" href={a.build.url} target="_blank" rel="noreferrer">
                  <Dot tone={runTone(a.build)} />{" "}
                  {runLabel({ status: a.build.status, conclusion: a.build.conclusion })}
                  <span class="muted">
                    {" "}
                    · {short(a.build.sha)} · {ago(a.build.updated)}
                  </span>
                </a>
              ) : (
                <span class="muted">no {props.board.workflow} runs</span>
              )}
            </dd>
          </div>
        </dl>

        {a.commits.length > 0 && (
          <div class="release-commits">
            <div class="eyebrow">
              Not running yet
              {a.diverged && " · the running image has commits the branch doesn't"}
            </div>
            <ul>
              {commits.map((c) => (
                <li key={c.sha}>
                  <a class="mono link" href={c.url} target="_blank" rel="noreferrer">
                    {short(c.sha)}
                  </a>
                  <span class="release-commit-msg">{c.message}</span>
                  <span class="muted">{ago(c.time)}</span>
                </li>
              ))}
            </ul>
            {a.commits.length > commits.length && (
              <button class="link-btn" onClick={() => setAllCommits(true)}>
                {a.commits.length - commits.length} more
              </button>
            )}
            {a.behind > a.commits.length && a.compare_url && (
              <a class="link muted" href={a.compare_url} target="_blank" rel="noreferrer">
                All {a.behind} on GitHub
              </a>
            )}
          </div>
        )}

        {a.prs.length > 0 && (
          <div class="release-prs">
            {a.prs.map((p) => (
              <PRRow key={p.number} pr={p}>
                <PRButtons pr={p} readOnly={props.readOnly} open={actions.open} />
              </PRRow>
            ))}
          </div>
        )}

        {a.ships.map((sh) => (
          <ShipNote key={sh.id} ship={sh} readOnly={props.readOnly} onChange={props.onChange} />
        ))}
        <AppButtons app={a} readOnly={props.readOnly} open={actions.open} />
        {actions.dialog}
        {a.errors.length > 0 && (
          <div class="note note-warn">
            {a.errors.map((e) => (
              <div key={e}>{e}</div>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

function runTone(r: { status: string; conclusion?: string }): Tone {
  if (r.status !== "completed") return "accent";
  if (r.conclusion === "success") return "good";
  if (r.conclusion === "skipped" || r.conclusion === "neutral") return "";
  return "bad";
}

function PRRow(props: { pr: PullRequest; children?: ComponentChildren }) {
  const p = props.pr;
  return (
    <div class="pr-row">
      <div class="pr-main">
        <a class="pr-title" href={p.url} target="_blank" rel="noreferrer">
          <span class="mono muted">#{p.number}</span> {p.title}
        </a>
        <span class="list-sub">
          <span class="mono">{p.branch}</span> · {p.author} · {ago(p.created)}
          {p.review === "approved" && " · approved"}
        </span>
        {p.runs.length > 0 && (
          <span class="pr-runs">
            {p.runs.map((r) => (
              <a key={r.id} href={r.url} target="_blank" rel="noreferrer" title={runLabel(r)}>
                <Dot tone={runTone(r)} /> {r.name}
              </a>
            ))}
          </span>
        )}
      </div>
      <span class={`chip tone-chip-${PR_TONE[p.state]}`}>{prStateLabel(p.state)}</span>
      {props.children}
    </div>
  );
}
