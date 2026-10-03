import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { api, type AuditQuery } from "../api";
import { useData } from "../hooks";
import { ACTION_LABEL, ago, auditTone } from "../lib";
import type { AuditEvent } from "../types";
import { Dot, ErrorNote } from "./ui";

const ACTIONS: [string, string][] = [
  ["", "All actions"],
  ["deploy", "Deploys"],
  ["compose.save", "Compose edits"],
  ["env.save", "Environment edits"],
  ["update.apply", "Updates"],
  ["rollback.", "Rollbacks"],
  ["release.", "Release board"],
  ["git.", "Git"],
  ["stack.", "Stacks added"],
];
const TRIGGERS: [string, string][] = [
  ["", "From anywhere"],
  ["ui", "Browser"],
  ["api", "API token"],
  ["foyer", "Foyer"],
  ["releases", "Release board"],
  ["auto", "Update policy"],
];
const RESULTS: [string, string][] = [
  ["", "Any result"],
  ["ok", "Succeeded"],
  ["failed", "Failed"],
  ["running", "Running"],
];

const PAGE = 100;

/** Everything done through Hoist, newest first. */
export function AuditPage() {
  const [query, setQuery] = useState<AuditQuery>({});
  const [older, setOlder] = useState<AuditEvent[]>([]);
  const [more, setMore] = useState<{ busy: boolean; error: string; done: boolean }>({
    busy: false,
    error: "",
    done: false,
  });
  const { data: stacks } = useData(api.stackNames, 0);
  const { data: events, error } = useData(() => api.audit({ ...query, limit: PAGE }), 10_000, [
    JSON.stringify(query),
  ]);

  const set = (key: keyof AuditQuery) => (e: Event) => {
    setOlder([]);
    setMore({ busy: false, error: "", done: false });
    setQuery({ ...query, [key]: (e.target as HTMLSelectElement).value || undefined });
  };

  const all = [...(events ?? []), ...older];
  const loadMore = async () => {
    setMore({ ...more, busy: true, error: "" });
    try {
      const page = await api.audit({ ...query, before: all[all.length - 1].id, limit: PAGE });
      setOlder([...older, ...page]);
      setMore({ busy: false, error: "", done: page.length < PAGE });
    } catch (e) {
      setMore({ ...more, busy: false, error: (e as Error).message });
    }
  };

  return (
    <div class="page">
      <header class="page-head">
        <div class="eyebrow eyebrow-accent">Audit log</div>
        <h1 class="page-title">Activity</h1>
        <p class="muted page-lede">
          Every deploy, edit, commit and update made through Hoist, and where it came from. Values
          from .env are never recorded, only which keys changed.
        </p>
        <div class="filters">
          <Select label="Stack" value={query.stack} onChange={set("stack")}>
            <option value="">All stacks</option>
            {stacks?.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </Select>
          <Select label="Action" value={query.action} onChange={set("action")} options={ACTIONS} />
          <Select
            label="Trigger"
            value={query.trigger}
            onChange={set("trigger")}
            options={TRIGGERS}
          />
          <Select label="Result" value={query.result} onChange={set("result")} options={RESULTS} />
        </div>
      </header>
      {error && <ErrorNote>{error}</ErrorNote>}
      {!events && !error && <div class="skeleton list-skeleton" />}
      {events && !all.length && <div class="empty">Nothing recorded yet.</div>}
      {all.length > 0 && (
        <div class="list audit-list">
          {all.map((e) => (
            <AuditRow key={e.id} event={e} />
          ))}
        </div>
      )}
      {events && events.length === PAGE && !more.done && (
        <div class="load-more">
          <button class="btn" disabled={more.busy} onClick={loadMore}>
            {more.busy ? "Loading…" : "Older"}
          </button>
          {more.error && <span class="tone-bad">{more.error}</span>}
        </div>
      )}
    </div>
  );
}

function Select(props: {
  label: string;
  value: string | undefined;
  onChange: (e: Event) => void;
  options?: [string, string][];
  children?: ComponentChildren;
}) {
  return (
    <label class="filter">
      <span class="eyebrow">{props.label}</span>
      <select class="input" value={props.value ?? ""} onChange={props.onChange}>
        {props.children}
        {props.options?.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </select>
    </label>
  );
}

function AuditRow(props: { event: AuditEvent }) {
  const e = props.event;
  const title = (
    <>
      <span class="list-title">
        {ACTION_LABEL[e.action] ?? e.action} <span class="muted">·</span> {e.stack}
        {e.services.length > 0 && <span class="muted"> · {e.services.join(", ")}</span>}
      </span>
      {e.detail && <span class="audit-detail">{e.detail}</span>}
      {e.error && <span class="audit-detail tone-bad">{e.error}</span>}
      <span class="list-sub">
        <time dateTime={e.time} title={new Date(e.time).toLocaleString()}>
          {ago(e.time)}
        </time>{" "}
        · from {e.trigger}
        {e.commit && (
          <>
            {" "}
            · at <span class="mono">{e.commit.slice(0, 7)}</span>
          </>
        )}
        {e.result === "running" && " · running"}
      </span>
    </>
  );
  return e.job ? (
    <a class="list-row" href={`/jobs/${e.job}`}>
      <Dot tone={auditTone(e)} />
      <span class="list-main">{title}</span>
    </a>
  ) : (
    <div class="list-row">
      <Dot tone={auditTone(e)} />
      <span class="list-main">{title}</span>
    </div>
  );
}
