import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, imageVersion, jobSummary, jobTime, when } from "../lib";
import { navigate } from "../router";
import type { Commit, ComposeFile, DeployRecord, Job, StackInfo } from "../types";
import { CodeEditor } from "./CodeEditor";
import { restoreVersion } from "./ComposeTab";
import { RollbackDialog } from "./Rollback";
import { Dialog, Dot, ErrorNote } from "./ui";

/** The compose file's commits, with a way back to any of them. */
export function HistoryTab(props: { stack: StackInfo; readOnly: boolean }) {
  const { stack } = props;
  const { data: commits, error } = useData(() => api.history(stack.name), 0, [stack.name]);
  const [open, setOpen] = useState<Commit | null>(null);

  if (!stack.git)
    return <div class="empty">This stack isn't in a git repo, so there's no history.</div>;
  if (!commits)
    return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton list-skeleton" />;
  if (!commits.length) return <div class="empty">No commits yet.</div>;

  return (
    <div class="list">
      {commits.map((c, i) => (
        <button key={c.hash} class="list-row" onClick={() => setOpen(c)}>
          <span class="mono tone-accent">{c.short}</span>
          <span class="list-main">
            <span class="list-title">{c.subject}</span>
            <span class="list-sub">
              {c.author} · {ago(c.time)}
              {i === 0 && " · current"}
              {c.path !== commits[0].path && ` · as ${c.path}`}
            </span>
          </span>
        </button>
      ))}
      {open && (
        <VersionDialog
          stack={stack}
          commit={open}
          readOnly={props.readOnly}
          onClose={() => setOpen(null)}
        />
      )}
    </div>
  );
}

function VersionDialog(props: {
  stack: StackInfo;
  commit: Commit;
  readOnly: boolean;
  onClose: () => void;
}) {
  const { data: file, error } = useData<ComposeFile>(
    () => api.historyFile(props.stack.name, props.commit),
    0,
    [props.commit.hash],
  );
  const restore = () => {
    restoreVersion(props.stack.name, file!.content, props.commit.short);
    navigate({ page: "stack", name: props.stack.name, tab: "compose" });
  };
  return (
    <Dialog
      title={props.commit.subject}
      wide
      onClose={props.onClose}
      footer={
        <>
          <span class="muted">
            {props.commit.short} · {props.commit.author} · {ago(props.commit.time)}
          </span>
          <span class="spacer" />
          {!props.readOnly && (
            <button class="btn btn-primary" disabled={!file} onClick={restore}>
              Open in the editor
            </button>
          )}
        </>
      }
    >
      {error && <ErrorNote>{error}</ErrorNote>}
      {file ? (
        <CodeEditor value={file.content} readOnly height="56dvh" />
      ) : (
        <div class="skeleton diff-skeleton" />
      )}
      {!props.readOnly && (
        <p class="muted">
          Opening it in the editor lets you review the difference and save it as a new commit, then
          deploy.
        </p>
      )}
    </Dialog>
  );
}

/**
 * Deploys, newest first: running ones, then the finished ones Hoist
 * recorded, marked good once they ran without trouble. Any good one other
 * than the current can be deployed again (a rollback).
 */
export function DeploysTab(props: { stack: StackInfo; readOnly: boolean }) {
  const name = props.stack.name;
  const { data, error } = useData(
    () => Promise.all([api.jobs(name, 100), api.deploys(name)]),
    5000,
    [name],
  );
  const [again, setAgain] = useState("");
  if (!data) return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton list-skeleton" />;
  const [jobs, records] = data;
  const byId = new Map(jobs.map((j) => [j.id, j]));
  const running = jobs.filter((j) => j.state === "running");
  if (!running.length && !records.length) {
    return <div class="empty">No deploys through Hoist yet.</div>;
  }
  return (
    <div class="list">
      {running.map((j) => (
        <a key={j.id} class="list-row" href={`/jobs/${j.id}`}>
          <Dot tone="accent" />
          <span class="list-main">
            <span class="list-title">{jobSummary(j)}</span>
            <span class="list-sub">
              {ago(j.started)} · from {j.trigger}
            </span>
          </span>
        </a>
      ))}
      {records.map((d) => (
        <DeployRow
          key={d.job}
          record={d}
          job={byId.get(d.job)}
          readOnly={props.readOnly || !!props.stack.active}
          onAgain={() => setAgain(d.job)}
        />
      ))}
      {again && <RollbackDialog stack={name} to={again} onClose={() => setAgain("")} />}
    </div>
  );
}

function DeployRow(props: {
  record: DeployRecord;
  job?: Job;
  readOnly: boolean;
  onAgain: () => void;
}) {
  const d = props.record;
  const j = props.job;
  const baseline = d.trigger === "baseline";
  const title = baseline
    ? "The stack as Hoist first saw it"
    : j
      ? jobSummary(j)
      : d.result === "failed"
        ? "Failed"
        : "Deployed";
  const own = d.images.filter((i) => i.revision);
  const body = (
    <span class="list-main">
      <span class={`list-title ${d.result === "failed" ? "tone-bad" : ""}`}>{title}</span>
      <span class="list-sub">
        {when(d.time)} · {ago(d.time)}
        {!baseline && ` · from ${d.trigger}`}
        {d.services.length ? ` · only ${d.services.join(", ")}` : ""}
        {d.commit && ` · at ${d.commit}${d.dirty ? " + uncommitted changes" : ""}`}
        {d.rollback && ` · rolled back to ${when(jobTime(d.rollback))}`}
      </span>
      {own.length > 0 && (
        <span class="list-sub mono">
          {own.map((i) => `${i.service}@${imageVersion(i)}`).join("  ")}
        </span>
      )}
    </span>
  );
  return (
    <div class="list-row deploy-row">
      <Dot tone={d.result === "failed" ? "bad" : d.good_at ? "good" : ""} />
      {baseline ? body : <a href={`/jobs/${d.job}`}>{body}</a>}
      <span class="spacer" />
      {d.current && <span class="chip chip-accent">current</span>}
      {d.good_at ? (
        <span class="chip" title={`Ran without trouble; good since ${when(d.good_at)}`}>
          good
        </span>
      ) : (
        d.result === "ok" &&
        d.current && (
          <span class="chip" title="Marked good once it has run without trouble for a while">
            proving
          </span>
        )
      )}
      {!d.current && d.result === "ok" && !props.readOnly && (
        <button class="btn btn-small" onClick={props.onAgain}>
          Deploy this version again
        </button>
      )}
    </div>
  );
}
