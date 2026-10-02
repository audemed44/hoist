import { useEffect, useRef, useState } from "preact/hooks";
import { api } from "../api";
import { ago, duration, jobSummary, jobTime, when } from "../lib";
import type { Job } from "../types";
import { Dot, ErrorNote } from "./ui";

/**
 * A deploy and its live log. When Hoist deploys itself the connection drops
 * while its container is replaced; the page keeps retrying until the new one
 * answers, then picks the log up again.
 */
export function JobPage(props: { id: string }) {
  const [job, setJob] = useState<Job | null>(null);
  const [log, setLog] = useState("");
  const [error, setError] = useState("");
  const [reconnecting, setReconnecting] = useState(false);
  const pre = useRef<HTMLPreElement>(null);
  const follow = useRef(true);

  useEffect(() => {
    let stopped = false;
    const controller = new AbortController();

    const stream = async () => {
      const res = await fetch(api.jobLogUrl(props.id), { signal: controller.signal });
      if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);
      setReconnecting(false);
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let text = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        text += decoder.decode(value, { stream: true });
        setLog(text);
      }
    };

    const run = async () => {
      for (let attempt = 0; !stopped; attempt++) {
        try {
          const j = await api.job(props.id);
          setJob(j);
          await stream();
          const final = await api.job(props.id);
          setJob(final);
          if (final.state !== "running") return;
        } catch (e) {
          if (stopped) return;
          if ((e as { status?: number }).status === 404) {
            setError("There's no such deploy.");
            return;
          }
          setReconnecting(true);
        }
        await new Promise((r) => setTimeout(r, Math.min(1000 + attempt * 500, 4000)));
      }
    };
    run();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [props.id]);

  // Keep the newest output in view unless the reader scrolled up.
  useEffect(() => {
    const el = pre.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [log]);

  if (error) return <ErrorNote>{error}</ErrorNote>;
  if (!job) return <div class="skeleton page-skeleton" />;

  const tone = job.state === "running" ? "accent" : job.state === "failed" ? "bad" : "good";
  return (
    <div class="page">
      <header class="page-head">
        <div class="eyebrow">
          <a class="link" href="/">
            Stacks
          </a>
          <span>/</span>
          <a class="link" href={`/stacks/${job.stack}/deploys`}>
            {job.stack}
          </a>
          <span>/</span>
          <span>Deploy</span>
        </div>
        <h1 class="page-title page-title-small">
          <Dot tone={tone} />{" "}
          {job.state === "running" ? "Deploying" : job.state === "failed" ? "Failed" : "Deployed"}
        </h1>
        <p class={`job-summary tone-${tone}`}>{jobSummary(job)}</p>
        <div class="job-meta muted">
          <span>Started {ago(job.started)}</span>
          {job.finished && <span>took {duration(job)}</span>}
          <span>from {job.trigger}</span>
          {job.services?.length ? <span>only {job.services.join(", ")}</span> : null}
          {job.commit && (
            <span class="mono">
              at {job.commit}
              {job.dirty && " + uncommitted changes"}
            </span>
          )}
          {job.self && <span>ran in a helper container</span>}
          {job.rollback && (
            <span>
              rolled back to{" "}
              <a class="link" href={`/jobs/${job.rollback}`}>
                {when(jobTime(job.rollback))}
              </a>
            </span>
          )}
          {job.pinned && !job.rollback && <span>images pinned by digest</span>}
        </div>
        {job.result && job.result.updated.length > 0 && (
          <p class="muted">New images: {job.result.updated.join(", ")}</p>
        )}
        {reconnecting && (
          <div class="note note-accent">
            {job.self
              ? "Hoist is restarting with the new version. Reconnecting…"
              : "Lost the connection. Reconnecting…"}
          </div>
        )}
      </header>
      <pre
        ref={pre}
        class="log"
        onScroll={(e) => {
          const el = e.currentTarget;
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {log || (job.state === "running" ? "Waiting for output…" : "No output.")}
      </pre>
    </div>
  );
}
