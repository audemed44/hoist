import { GitMerge, RotateCcw, Rocket, Undo2 } from "lucide-preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import { prStateLabel } from "../lib";
import { navigate } from "../router";
import type { PullRequest, ReleaseApp } from "../types";
import { RollbackDialog } from "./Rollback";
import { Dialog } from "./ui";

/** How long after a deploy an app's card offers to roll it back. */
const ROLLBACK_WINDOW = 2 * 3600_000;

type Pending =
  | { kind: "merge"; pr: PullRequest }
  | { kind: "rerun"; pr?: PullRequest }
  | { kind: "deploy" }
  | { kind: "rollback" };

/** The board's actions for an app and its pull requests, each confirmed. */
export function useReleaseActions(app: ReleaseApp, onDone: () => void) {
  const [pending, setPending] = useState<Pending | null>(null);
  const close = () => setPending(null);
  const dialog =
    pending &&
    (pending.kind === "rollback" ? (
      <RollbackDialog
        stack={app.stack}
        onClose={() => {
          close();
          onDone();
        }}
      />
    ) : pending.kind === "merge" ? (
      <MergeDialog app={app} pr={pending.pr} onClose={close} onDone={onDone} />
    ) : pending.kind === "rerun" ? (
      <RerunDialog app={app} pr={pending.pr} onClose={close} onDone={onDone} />
    ) : (
      <DeployAppDialog app={app} onClose={close} />
    ));
  return { dialog, open: setPending };
}

/** The app's own buttons: deploy a waiting image, re-run a failed build, roll back. */
export function AppButtons(props: {
  app: ReleaseApp;
  readOnly: boolean;
  open: (p: Pending) => void;
}) {
  const a = props.app;
  if (props.readOnly) return null;
  const recent =
    a.last?.state === "done" && Date.now() - new Date(a.last.started).getTime() < ROLLBACK_WINDOW;
  return (
    <div class="pr-actions release-actions">
      {a.state === "build-failed" && (
        <button class="btn" onClick={() => props.open({ kind: "rerun" })}>
          <RotateCcw size={14} /> Re-run build
        </button>
      )}
      {recent && !a.pinned && (
        <button class="btn" onClick={() => props.open({ kind: "rollback" })} disabled={!!a.active}>
          <Undo2 size={14} /> Roll back
        </button>
      )}
      {a.pinned ? (
        <a class="btn" href={`/stacks/${a.stack}`}>
          Rolled back: resume on {a.stack}
        </a>
      ) : (
        a.state !== "deployed" && (
          <button
            class={`btn ${a.state === "ready" ? "btn-primary" : ""}`}
            onClick={() => props.open({ kind: "deploy" })}
            disabled={!!a.active}
          >
            <Rocket size={14} /> {a.active ? "Deploying…" : "Deploy"}
          </button>
        )
      )}
    </div>
  );
}

/** A pull request's buttons: merge when it can be, re-run what failed. */
export function PRButtons(props: {
  pr: PullRequest;
  readOnly: boolean;
  open: (p: Pending) => void;
}) {
  const p = props.pr;
  if (props.readOnly) return null;
  return (
    <span class="pr-actions">
      {p.state === "ci-failing" && (
        <button class="btn btn-small" onClick={() => props.open({ kind: "rerun", pr: p })}>
          <RotateCcw size={13} /> Re-run failed
        </button>
      )}
      {p.state === "conflict" ? (
        <a class="btn btn-small" href={p.url} target="_blank" rel="noreferrer">
          Resolve on GitHub
        </a>
      ) : (
        p.state !== "draft" && (
          <button
            class={`btn btn-small ${p.state === "ready" ? "btn-primary" : ""}`}
            onClick={() => props.open({ kind: "merge", pr: p })}
          >
            <GitMerge size={13} /> Merge
          </button>
        )
      )}
    </span>
  );
}

function useRun() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, run };
}

function MergeDialog(props: {
  app: ReleaseApp;
  pr: PullRequest;
  onClose: () => void;
  onDone: () => void;
}) {
  const { app, pr } = props;
  const green = pr.state === "ready";
  const [force, setForce] = useState(false);
  const [done, setDone] = useState("");
  const { busy, error, run } = useRun();
  const merge = () =>
    run(async () => {
      const res = await api.merge(app.repo, app.stack, pr.number, !green);
      setDone(
        `Merged as ${res.sha.slice(0, 7)}.` +
          (res.deleted
            ? ` Deleted ${pr.branch}.`
            : res.delete_error
              ? ` The branch wasn't deleted: ${res.delete_error}`
              : ""),
      );
      props.onDone();
    });
  return (
    <Dialog
      title={`Merge #${pr.number}`}
      onClose={props.onClose}
      footer={
        <>
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            {done ? "Close" : "Cancel"}
          </button>
          {!done && (
            <button
              class={`btn ${green ? "btn-primary" : "btn-danger"}`}
              onClick={merge}
              disabled={busy || (!green && !force)}
            >
              <GitMerge size={14} /> {green ? "Rebase and merge" : "Merge anyway"}
            </button>
          )}
        </>
      }
    >
      <p>
        <strong>{pr.title}</strong>
      </p>
      <p class="muted">
        Rebases <span class="mono">{pr.branch}</span> onto <span class="mono">{app.branch}</span> of{" "}
        {app.repo} (no merge commit)
        {pr.same_repo ? ", then deletes the branch." : "."} The image build starts on GitHub; deploy
        it from the board once it's published.
      </p>
      {!green && (
        <label class="check note note-warn">
          <input type="checkbox" checked={force} onChange={() => setForce(!force)} />
          <span>
            It's not ready: <strong>{prStateLabel(pr.state).toLowerCase()}</strong>. Merge it
            anyway.
          </span>
        </label>
      )}
      {done && <p class="note note-accent">{done}</p>}
      {error && <div class="form-error">{error}</div>}
    </Dialog>
  );
}

function RerunDialog(props: {
  app: ReleaseApp;
  pr?: PullRequest;
  onClose: () => void;
  onDone: () => void;
}) {
  const { app, pr } = props;
  const { busy, error, run } = useRun();
  const failed = pr
    ? pr.runs.filter(
        (r) =>
          r.status === "completed" &&
          r.conclusion !== "success" &&
          r.conclusion !== "skipped" &&
          r.conclusion !== "neutral",
      )
    : [];
  const rerun = async () => {
    if (await run(() => api.rerun(app.repo, app.stack, pr?.number ?? 0))) {
      props.onDone();
      props.onClose();
    }
  };
  return (
    <Dialog
      title={pr ? `Re-run #${pr.number}'s failed checks` : "Re-run the image build"}
      onClose={props.onClose}
      footer={
        <>
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn btn-primary" onClick={rerun} disabled={busy}>
            <RotateCcw size={14} /> Re-run failed jobs
          </button>
        </>
      }
    >
      <p class="muted">
        {pr
          ? `Re-runs the failed jobs of ${failed.map((r) => r.name).join(", ") || "its checks"} on GitHub.`
          : `Re-runs the failed jobs of ${app.repo}'s image build on ${app.branch}.`}
      </p>
      {error && <div class="form-error">{error}</div>}
    </Dialog>
  );
}

function DeployAppDialog(props: { app: ReleaseApp; onClose: () => void }) {
  const a = props.app;
  const { busy, error, run } = useRun();
  const deploy = () =>
    run(async () => {
      const job = await api.deployApp(a.repo, a.stack);
      navigate({ page: "job", id: job.id });
    });
  const name = a.repo.split("/")[1];
  return (
    <Dialog
      title={`Deploy ${name}`}
      onClose={props.onClose}
      footer={
        <>
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn btn-primary" onClick={deploy} disabled={busy}>
            <Rocket size={14} /> Pull and deploy
          </button>
        </>
      }
    >
      <p class="muted">
        Pulls <span class="mono">{a.image}</span> and redeploys{" "}
        {a.services.length === 1 ? "the service" : "the services"}{" "}
        <span class="mono">{a.services.join(", ")}</span> in {a.stack}. The rest of the stack is
        left as it is.
      </p>
      {a.state !== "ready" && (
        <p class="note">
          {a.state === "building"
            ? "The new image is still being built; this pulls whatever the tag points to now."
            : "No newer image was found, so this may change nothing."}
        </p>
      )}
      {error && <div class="form-error">{error}</div>}
    </Dialog>
  );
}
