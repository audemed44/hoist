import { Rocket } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { api } from "../api";
import { CHANGE_LABEL } from "../lib";
import { navigate } from "../router";
import type { GitStatus, StackInfo } from "../types";
import { Dialog } from "./ui";

/**
 * Confirms a deploy, listing what the compose file says will change. New
 * images behind the same tag only show up once they're pulled.
 *
 * A deploy runs the files on disk. The dialog fetches when it opens, and
 * when something was merged that isn't here yet (a PR merged on GitHub),
 * it offers to pull it first, ticked.
 */
export function DeployDialog(props: { stack: StackInfo; service?: string; onClose: () => void }) {
  const s = props.stack;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [git, setGit] = useState<GitStatus | undefined>(s.git);
  const [pull, setPull] = useState(true);
  useEffect(() => {
    if (!s.git) return;
    api
      .git(s.name, "fetch")
      .then(setGit)
      .catch(() => {});
  }, [s.name]);
  const behind = git?.behind ?? 0;
  const diverged = behind > 0 && (git?.ahead ?? 0) > 0;
  const pulling = behind > 0 && !diverged && pull;
  const scope = props.service ? s.services.filter((x) => x.name === props.service) : s.services;
  const changes = scope.filter((x) => x.change);

  const deploy = async () => {
    setBusy(true);
    setError("");
    try {
      const job = await api.deploy(s.name, props.service ? [props.service] : undefined, pulling);
      navigate({ page: "job", id: job.id });
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <Dialog
      title={props.service ? `Deploy ${props.service}` : `Deploy ${s.name}`}
      onClose={props.onClose}
      footer={
        <>
          {behind > 0 && !diverged && (
            <label class="check">
              <input
                type="checkbox"
                checked={pull}
                onChange={(e) => setPull(e.currentTarget.checked)}
              />
              Pull {commits(behind)} first
            </label>
          )}
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
        Pulls the latest images, then runs{" "}
        <code>docker compose up -d{props.service ? ` ${props.service}` : " --remove-orphans"}</code>
        . Only services whose config or image changed are recreated.
      </p>
      {changes.length ? (
        <ul class="plan">
          {changes.map((c) => (
            <li key={c.name}>
              <span class={`chip chip-${c.change}`}>
                {CHANGE_LABEL[c.change as keyof typeof CHANGE_LABEL]}
              </span>
              <span class="mono">{c.name}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p>The compose file matches what's running. Only new images will be picked up.</p>
      )}
      {behind > 0 && !diverged && (
        <p class={pulling ? "note" : "note note-warn"}>
          {git?.upstream ?? "The remote"} has {commits(behind)} that{" "}
          {behind === 1 ? "isn't" : "aren't"} here yet (merged on GitHub?).{" "}
          {pulling
            ? `${behind === 1 ? "It's" : "They're"} pulled first, so the deploy runs ${behind === 1 ? "it" : "them"}; the list above is from before the pull.`
            : "Without pulling, the deploy runs the files as they are on disk."}
        </p>
      )}
      {diverged && (
        <p class="note note-warn">
          {git?.branch} has {commits(git!.ahead)} of its own and is {behind} behind{" "}
          {git?.upstream ?? "the remote"}, so it can't be pulled here. The deploy runs the files on
          disk; push or sort the branch out to get the merged changes.
        </p>
      )}
      {s.git?.modified && (
        <p class="note note-warn">
          The compose file has changes that aren't committed; they'll be deployed as they are.
        </p>
      )}
      {s.self && (
        <p class="note">
          This is Hoist's own stack. A helper container runs the deploy, so it carries on while
          Hoist restarts; this page reconnects when it's back.
        </p>
      )}
      {error && <div class="form-error">{error}</div>}
    </Dialog>
  );
}

const commits = (n: number) => (n === 1 ? "1 commit" : `${n} commits`);
