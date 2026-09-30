import {
  ArrowDownToLine,
  ArrowUpFromLine,
  GitCommitHorizontal,
  RefreshCw,
  Rocket,
} from "lucide-preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, gitLabel, jobSummary } from "../lib";
import { href, type Tab, TABS } from "../router";
import type { StackInfo } from "../types";
import { ComposeTab } from "./ComposeTab";
import { DeployDialog } from "./DeployDialog";
import { DriftDialog } from "./Drift";
import { EnvTab } from "./EnvTab";
import { DeploysTab, HistoryTab } from "./HistoryTabs";
import { ServicesTab } from "./ServicesTab";
import { ErrorNote } from "./ui";

const TAB_LABEL: Record<Tab, string> = {
  services: "Services",
  compose: "Compose",
  env: "Environment",
  history: "History",
  deploys: "Deploys",
};

export function StackPage(props: { name: string; tab: Tab; readOnly: boolean }) {
  const { data: stack, error, reload } = useData(() => api.stack(props.name), 8000, [props.name]);
  const [deploy, setDeploy] = useState<{ service?: string } | null>(null);

  if (!stack) {
    return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton page-skeleton" />;
  }
  const git = gitLabel(stack.git);

  return (
    <div class="page">
      <header class="page-head">
        <div class="eyebrow">
          <a class="link" href="/">
            Stacks
          </a>
          <span>/</span>
          <span class="mono">{stack.project}</span>
          {stack.self && <span class="chip">Hoist runs here</span>}
        </div>
        <h1 class="page-title">{stack.name}</h1>
        <div class="page-bar">
          <div class="figures">
            <div class={`figure ${stack.counts.running < stack.counts.services ? "warn" : ""}`}>
              <div class="figure-value">
                {stack.counts.running}
                <span class="figure-unit">/{stack.counts.services}</span>
              </div>
              <div class="eyebrow figure-label">Running</div>
            </div>
            <div class={`figure ${stack.counts.pending ? "accent" : ""}`}>
              <div class="figure-value">{stack.counts.pending}</div>
              <div class="eyebrow figure-label">To deploy</div>
            </div>
          </div>
          <span class="spacer" />
          {!props.readOnly && (
            <button
              class="btn btn-primary btn-big"
              disabled={!!stack.active || !!stack.error}
              onClick={() => setDeploy({})}
            >
              <Rocket size={16} /> {stack.active ? "Deploying…" : "Deploy"}
            </button>
          )}
        </div>
        <GitBar stack={stack} readOnly={props.readOnly} onChange={reload} label={git} />
        {stack.active && (
          <a class="note note-accent" href={`/jobs/${stack.active.id}`}>
            Deploying since {ago(stack.active.started)}. Follow the log →
          </a>
        )}
        {!stack.active && stack.last?.state === "failed" && (
          <a class="note note-bad" href={`/jobs/${stack.last.id}`}>
            The last deploy failed {ago(stack.last.started)}: {jobSummary(stack.last)} →
          </a>
        )}
        {stack.error && <div class="note note-bad mono">{stack.error}</div>}
      </header>

      <nav class="tabs" aria-label="Stack sections">
        {TABS.map((t) => (
          <a
            key={t}
            class={`tab ${t === props.tab ? "active" : ""}`}
            href={href({ page: "stack", name: stack.name, tab: t })}
          >
            {TAB_LABEL[t]}
            {t === "services" && stack.counts.pending > 0 && (
              <span class="tab-count">{stack.counts.pending}</span>
            )}
          </a>
        ))}
      </nav>

      {props.tab === "services" && (
        <ServicesTab
          stack={stack}
          readOnly={props.readOnly}
          onDeploy={(service) => setDeploy({ service })}
        />
      )}
      {props.tab === "compose" && (
        <ComposeTab stack={stack} readOnly={props.readOnly} onSaved={reload} />
      )}
      {props.tab === "env" && <EnvTab stack={stack} readOnly={props.readOnly} />}
      {props.tab === "history" && <HistoryTab stack={stack} readOnly={props.readOnly} />}
      {props.tab === "deploys" && <DeploysTab stack={stack} />}

      {deploy && (
        <DeployDialog
          stack={stack}
          service={deploy.service}
          onClose={() => {
            setDeploy(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

/** Branch state, with the git action that state calls for. */
function GitBar(props: {
  stack: StackInfo;
  readOnly: boolean;
  label: { text: string; tone: string };
  onChange: () => void;
}) {
  const { stack } = props;
  const git = stack.git;
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [committing, setCommitting] = useState(false);

  const run = async (name: string, fn: () => Promise<unknown>) => {
    setBusy(name);
    setError("");
    try {
      await fn();
      props.onChange();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  };

  if (!git) {
    return (
      <div class="git-bar">
        <span class="muted">
          {stack.git_error ?? "Not in a git repo: edits are saved without history."}
        </span>
      </div>
    );
  }
  return (
    <div class="git-bar">
      <span class={`tone-${props.label.tone}`}>{props.label.text}</span>
      {git.fetch_error && (
        <span class="tone-bad" title={git.fetch_error}>
          fetch failed
        </span>
      )}
      {git.fetched_at && <span class="muted">checked {ago(git.fetched_at)}</span>}
      <span class="spacer" />
      <button
        class="btn btn-ghost btn-small"
        disabled={!!busy}
        onClick={() => run("fetch", () => api.git(stack.name, "fetch"))}
        title="Check the remote for new commits"
      >
        <RefreshCw size={13} class={busy === "fetch" ? "spin" : ""} /> Check
      </button>
      {git.modified && (
        <button class="btn btn-small" disabled={!!busy} onClick={() => setCommitting(true)}>
          <GitCommitHorizontal size={13} /> Review changes
        </button>
      )}
      {!props.readOnly && git.behind > 0 && (
        <button
          class="btn btn-small"
          disabled={!!busy}
          onClick={() => run("pull", () => api.git(stack.name, "pull"))}
        >
          <ArrowDownToLine size={13} /> Pull {git.behind}
        </button>
      )}
      {!props.readOnly && git.ahead > 0 && (
        <button
          class="btn btn-small"
          disabled={!!busy}
          onClick={() => run("push", () => api.git(stack.name, "push"))}
        >
          <ArrowUpFromLine size={13} /> Push {git.ahead}
        </button>
      )}
      {error && <div class="form-error git-error">{error}</div>}
      {git.modified && (
        <div class="note note-warn git-drift">
          The compose file was changed on the server and differs from the last commit.{" "}
          <button class="link-btn" onClick={() => setCommitting(true)}>
            Review
          </button>
        </div>
      )}
      {committing && (
        <DriftDialog
          stack={stack.name}
          readOnly={props.readOnly}
          onClose={() => setCommitting(false)}
          onDone={() => {
            setCommitting(false);
            props.onChange();
          }}
        />
      )}
    </div>
  );
}
