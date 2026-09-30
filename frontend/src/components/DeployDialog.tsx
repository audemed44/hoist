import { Rocket } from "lucide-preact";
import { useState } from "preact/hooks";
import { api } from "../api";
import { CHANGE_LABEL } from "../lib";
import { navigate } from "../router";
import type { StackInfo } from "../types";
import { Dialog } from "./ui";

/**
 * Confirms a deploy, listing what the compose file says will change. New
 * images behind the same tag only show up once they're pulled.
 */
export function DeployDialog(props: { stack: StackInfo; service?: string; onClose: () => void }) {
  const s = props.stack;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const scope = props.service ? s.services.filter((x) => x.name === props.service) : s.services;
  const changes = scope.filter((x) => x.change);

  const deploy = async () => {
    setBusy(true);
    setError("");
    try {
      const job = await api.deploy(s.name, props.service ? [props.service] : undefined);
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
