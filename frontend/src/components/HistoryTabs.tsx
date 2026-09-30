import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, duration, jobSummary } from "../lib";
import { navigate } from "../router";
import type { Commit, ComposeFile, StackInfo } from "../types";
import { CodeEditor } from "./CodeEditor";
import { restoreVersion } from "./ComposeTab";
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

export function DeploysTab(props: { stack: StackInfo }) {
  const { data: jobs, error } = useData(() => api.jobs(props.stack.name, 50), 5000, [
    props.stack.name,
  ]);
  if (!jobs) return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton list-skeleton" />;
  if (!jobs.length) return <div class="empty">No deploys through Hoist yet.</div>;
  return (
    <div class="list">
      {jobs.map((j) => (
        <a key={j.id} class="list-row" href={`/jobs/${j.id}`}>
          <Dot tone={j.state === "running" ? "accent" : j.state === "failed" ? "bad" : "good"} />
          <span class="list-main">
            <span class={`list-title ${j.state === "failed" ? "tone-bad" : ""}`}>
              {jobSummary(j)}
            </span>
            <span class="list-sub">
              {ago(j.started)}
              {j.finished && ` · took ${duration(j)}`} · from {j.trigger}
              {j.services?.length ? ` · only ${j.services.join(", ")}` : ""}
              {j.commit && ` · at ${j.commit}${j.dirty ? " + uncommitted changes" : ""}`}
            </span>
          </span>
        </a>
      ))}
    </div>
  );
}
