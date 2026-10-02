import { History, Undo2 } from "lucide-preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, imageVersion, jobTime, when } from "../lib";
import { navigate } from "../router";
import type { PinInfo, RollbackPlan } from "../types";
import { CodeEditor } from "./CodeEditor";
import { Dialog, ErrorNote } from "./ui";

/**
 * Rolls a stack back to an earlier deploy (by default the last good one
 * before the current): shows what changes per service and whether each
 * image can still be had, then pins the stack to those images by digest.
 */
export function RollbackDialog(props: { stack: string; to?: string; onClose: () => void }) {
  const { data: plan, error } = useData<RollbackPlan>(
    () => api.rollbackPlan(props.stack, props.to),
    0,
    [props.stack, props.to],
  );
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState("");

  const run = async () => {
    setBusy(true);
    setFailed("");
    try {
      const job = await api.rollback(props.stack, plan!.target.job);
      navigate({ page: "job", id: job.id });
    } catch (e) {
      setFailed((e as Error).message);
      setBusy(false);
    }
  };
  const changing = plan?.services.filter((s) => s.changes) ?? [];

  return (
    <Dialog
      title={plan ? `Roll back to ${when(plan.target.time)}` : "Roll back"}
      wide={!!plan?.compose}
      onClose={props.onClose}
      footer={
        <>
          {plan && (
            <span class="muted">
              {plan.target.trigger === "baseline"
                ? "as Hoist first saw it"
                : `deployed ${ago(plan.target.time)} from ${plan.target.trigger}`}
              {plan.target.commit && ` · ${plan.target.commit}`}
            </span>
          )}
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn btn-primary" onClick={run} disabled={busy || !plan || !!plan.problem}>
            <Undo2 size={14} /> Roll back
          </button>
        </>
      }
    >
      {error && <ErrorNote>{error}</ErrorNote>}
      {!plan && !error && <div class="skeleton list-skeleton" />}
      {plan && (
        <>
          <p class="muted">
            Redeploys the stack with each image pinned by digest, so it stays on this version until
            you resume. Nothing in the stack's repo changes.
          </p>
          <ul class="plan rollback-plan">
            {plan.services.map((s) => (
              <li key={s.service}>
                <span
                  class={`chip ${s.problem && s.pinned ? "chip-remove" : s.changes ? "chip-accent" : ""}`}
                >
                  {s.problem && s.pinned ? "gone" : s.changes ? "changes" : "same"}
                </span>
                <span class="rollback-svc">
                  <span class="mono">{s.service}</span>
                  <span class="list-sub mono">
                    {s.from ? imageVersion(s.from) : "not running"} → {imageVersion(s.to)}
                    {s.where === "registry" && " · pulled from the registry"}
                  </span>
                  {s.problem && (
                    <span class={`list-sub ${s.pinned ? "tone-bad" : ""}`}>{s.problem}</span>
                  )}
                </span>
              </li>
            ))}
          </ul>
          {!changing.length && !plan.compose && (
            <p class="note">That deploy ran the same images as now.</p>
          )}
          {plan.compose && plan.current && (
            <>
              <p class="muted">
                It ran an older compose file ({plan.target.commit}); the rollback uses that version
                (green) instead of the current one.
              </p>
              <CodeEditor
                value={plan.compose.content}
                original={plan.current.content}
                readOnly
                height="34dvh"
              />
            </>
          )}
          {plan.notes.map((n) => (
            <p key={n} class="note note-warn">
              {n}
            </p>
          ))}
          {plan.problem && <ErrorNote>{plan.problem}</ErrorNote>}
          {failed && <div class="form-error">{failed}</div>}
        </>
      )}
    </Dialog>
  );
}

/** A rolled-back stack: what it's pinned to, and the way back to :latest. */
export function PinBanner(props: {
  stack: string;
  pin: PinInfo;
  busy: boolean;
  readOnly: boolean;
}) {
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const resume = async () => {
    setBusy(true);
    setError("");
    try {
      const job = await api.resume(props.stack);
      navigate({ page: "job", id: job.id });
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };
  const n = Object.keys(props.pin.images).length;
  return (
    <div class="note note-warn pin-banner">
      <History size={15} />
      <span>
        <strong>Rolled back</strong> {ago(props.pin.at)} to the deploy of{" "}
        <a class="link" href={`/jobs/${props.pin.deploy}`}>
          {when(jobTime(props.pin.deploy))}
        </a>
        : {n} {n === 1 ? "image" : "images"} pinned by digest
        {props.pin.old_compose && `, older compose file (${props.pin.commit})`}. Deploys keep it
        there, and update checks skip it.
      </span>
      <span class="spacer" />
      {!props.readOnly &&
        (confirm ? (
          <span class="pin-confirm">
            <button class="btn btn-ghost btn-small" onClick={() => setConfirm(false)}>
              Cancel
            </button>
            <button
              class="btn btn-primary btn-small"
              disabled={busy || props.busy}
              onClick={resume}
            >
              Pull and deploy :latest
            </button>
          </span>
        ) : (
          <button class="btn btn-small" disabled={props.busy} onClick={() => setConfirm(true)}>
            Resume :latest
          </button>
        ))}
      {error && <div class="form-error">{error}</div>}
    </div>
  );
}
