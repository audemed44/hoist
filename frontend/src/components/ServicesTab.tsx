import { Rocket } from "lucide-preact";
import { useState } from "preact/hooks";
import { updateText, type Tone } from "../lib";
import type { ServiceState, ServiceUpdate, StackInfo } from "../types";
import { UpdateDialog } from "./Updates";
import { Dot } from "./ui";

function stateTone(s: ServiceState): Tone {
  const c = s.container;
  if (!c) return "";
  if (c.state === "running") return c.status.includes("unhealthy") ? "warn" : "good";
  if (c.state === "restarting") return "bad";
  return "";
}

export function ServicesTab(props: {
  stack: StackInfo;
  readOnly: boolean;
  onDeploy: (service: string) => void;
}) {
  const { stack } = props;
  const [updating, setUpdating] = useState<ServiceUpdate | null>(null);
  const updates = Object.fromEntries(stack.updates.map((u) => [u.service, u]));
  if (!stack.services.length) {
    return (
      <div class="empty">No services{stack.error ? " (the compose file has an error)" : ""}.</div>
    );
  }
  return (
    <div class="svc-list">
      <div class="svc-row svc-header eyebrow">
        <span>Service</span>
        <span>Image</span>
        <span>Status</span>
        <span>Pending</span>
        <span />
      </div>
      {stack.services.map((s) => (
        <div
          key={s.name}
          class={`svc-row ${s.container?.state === "running" ? "" : "svc-stopped"}`}
        >
          <span class="svc-name">
            <Dot tone={stateTone(s)} />
            <span>
              <span class="svc-title">{s.name}</span>
              {s.container && s.container.name !== s.name && (
                <span class="svc-sub mono">{s.container.name}</span>
              )}
            </span>
          </span>
          <span class="svc-image-cell">
            <span class="mono svc-image" title={s.image}>
              {s.image || <span class="muted">built locally</span>}
            </span>
            {updates[s.name] && (
              <button
                class="svc-update"
                disabled={props.readOnly || !!stack.active}
                onClick={() => setUpdating(updates[s.name])}
                title={props.readOnly ? "Read-only" : "Apply this update"}
              >
                ↑ {updateText(updates[s.name])}
              </button>
            )}
          </span>
          <span class="svc-status">
            {s.container ? s.container.status : <span class="muted">No container</span>}
          </span>
          <span>
            {s.change ? (
              <span class={`chip chip-${s.change}`}>{s.change}</span>
            ) : s.orphan ? (
              <span class="chip" title="Not in the compose file; remove_orphans is off">
                orphan
              </span>
            ) : (
              <span class="muted">up to date</span>
            )}
          </span>
          <span class="svc-actions">
            {!props.readOnly && !s.orphan && (
              <button
                class="icon-btn"
                title={`Pull and deploy only ${s.name}`}
                aria-label={`Deploy ${s.name}`}
                disabled={!!stack.active}
                onClick={() => props.onDeploy(s.name)}
              >
                <Rocket size={15} />
              </button>
            )}
          </span>
        </div>
      ))}
      {updating && (
        <UpdateDialog stack={stack.name} update={updating} onClose={() => setUpdating(null)} />
      )}
    </div>
  );
}
