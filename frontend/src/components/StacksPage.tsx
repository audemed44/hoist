import { ArrowUpRight, Rocket } from "lucide-preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, gitLabel, jobSummary, pendingText, stackTone } from "../lib";
import type { StackInfo } from "../types";
import { DeployDialog } from "./DeployDialog";
import { Dot, ErrorNote, SectionHead } from "./ui";

export function StacksPage(props: { readOnly: boolean }) {
  const { data: stacks, error, reload } = useData(api.stacks, 10_000);
  const [deploying, setDeploying] = useState<StackInfo | null>(null);

  if (!stacks) {
    return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton page-skeleton" />;
  }
  const running = stacks.reduce((n, s) => n + s.counts.running, 0);
  const total = stacks.reduce((n, s) => n + s.counts.services, 0);
  const pending = stacks.reduce((n, s) => n + s.counts.pending, 0);
  const last = stacks
    .map((s) => s.last)
    .filter((j) => j)
    .sort((a, b) => b!.started.localeCompare(a!.started))[0];

  return (
    <div class="page">
      <header class="page-head">
        <div class="eyebrow eyebrow-accent">
          {stacks.length} {stacks.length === 1 ? "stack" : "stacks"}
        </div>
        <h1 class="page-title">Stacks</h1>
        <div class="figures">
          <Figure
            value={running}
            unit={`/${total}`}
            label="Running"
            tone={running < total ? "warn" : ""}
          />
          <Figure value={pending} label="To deploy" tone={pending ? "accent" : ""} />
          {last && (
            <Figure
              value={ago(last.started)}
              label={`Last deploy · ${last.stack}`}
              tone={last.state === "failed" ? "bad" : ""}
            />
          )}
        </div>
      </header>
      {error && <ErrorNote>{error}</ErrorNote>}
      <div class="stack-list stagger">
        {stacks.map((s, i) => (
          <StackRow
            key={s.name}
            index={i + 1}
            stack={s}
            readOnly={props.readOnly}
            onDeploy={() => setDeploying(s)}
          />
        ))}
      </div>
      {deploying && (
        <DeployDialog
          stack={deploying}
          onClose={() => {
            setDeploying(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function Figure(props: { value: string | number; unit?: string; label: string; tone?: string }) {
  return (
    <div class={`figure ${props.tone ?? ""}`}>
      <div class="figure-value">
        {props.value}
        {props.unit && <span class="figure-unit">{props.unit}</span>}
      </div>
      <div class="eyebrow figure-label">{props.label}</div>
    </div>
  );
}

function StackRow(props: {
  index: number;
  stack: StackInfo;
  readOnly: boolean;
  onDeploy: () => void;
}) {
  const s = props.stack;
  const git = gitLabel(s.git);
  const changed = s.services.filter((svc) => svc.change);
  const job = s.active ?? s.last;
  return (
    <section class="stack-row">
      <SectionHead index={props.index} title={s.name}>
        <span class="eyebrow">
          <Dot tone={stackTone(s)} />
          {s.counts.running}/{s.counts.services} running
        </span>
      </SectionHead>
      <div class="stack-row-body">
        <dl class="facts">
          <div>
            <dt class="eyebrow">Project</dt>
            <dd class="mono">{s.project}</dd>
          </div>
          <div>
            <dt class="eyebrow">Folder</dt>
            <dd class="mono" title={`${s.path}/${s.file}`}>
              {s.path}
            </dd>
          </div>
          <div>
            <dt class="eyebrow">Git</dt>
            <dd class={`tone-${git.tone}`}>{git.text}</dd>
          </div>
          <div>
            <dt class="eyebrow">Last deploy</dt>
            <dd class={job?.state === "failed" ? "tone-bad" : ""}>
              {job ? (
                <a class="link" href={`/jobs/${job.id}`}>
                  {job.state === "running" ? "Deploying now" : ago(job.started)}
                  <span class="muted"> · {jobSummary(job)}</span>
                </a>
              ) : (
                <span class="muted">Not through Hoist yet</span>
              )}
            </dd>
          </div>
        </dl>
        {s.error ? (
          <div class="note note-bad mono">{s.error}</div>
        ) : (
          <div class="pending">
            <span class={changed.length ? "tone-accent" : "muted"}>
              {pendingText(changed.length)}
            </span>
            {changed.map((svc) => (
              <span key={svc.name} class={`chip chip-${svc.change}`}>
                {svc.change} {svc.name}
              </span>
            ))}
          </div>
        )}
        <div class="stack-row-actions">
          <a class="btn" href={`/stacks/${s.name}`}>
            Open <ArrowUpRight size={14} />
          </a>
          {!props.readOnly && (
            <button class="btn btn-primary" onClick={props.onDeploy} disabled={!!s.active}>
              <Rocket size={14} /> {s.active ? "Deploying…" : "Deploy"}
            </button>
          )}
        </div>
      </div>
    </section>
  );
}
