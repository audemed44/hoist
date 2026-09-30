import { ArrowUpCircle, RefreshCw } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { ago, shortDuration, tagOf } from "../lib";
import { navigate } from "../router";
import type { ServiceUpdate } from "../types";
import { Dialog } from "./ui";

/** When the registries were last asked, how often, and a way to ask now. */
export function UpdatesBar(props: { onChecked: () => void }) {
  const { data, reload } = useData(api.updates, 0);
  const [error, setError] = useState("");
  const checking = data?.checking ?? false;

  // While a check runs, follow it, then reload the page's data once.
  useEffect(() => {
    if (!checking) return;
    const timer = setInterval(async () => {
      const next = await api.updates().catch(() => null);
      if (next && !next.checking) {
        clearInterval(timer);
        reload();
        props.onChecked();
      }
    }, 2000);
    return () => clearInterval(timer);
  }, [checking]);

  const check = async () => {
    setError("");
    try {
      await api.checkUpdates();
    } catch (e) {
      setError((e as Error).message);
    }
    reload();
  };

  if (!data) return null;
  return (
    <div class="updates-bar">
      <span>
        {data.checked_at ? `Updates checked ${ago(data.checked_at)}` : "Updates not checked yet"}
      </span>
      <span class="muted">
        {data.every ? `every ${shortDuration(data.every)}` : "only when asked"}
        {data.auto !== "off" ? ` · applies ${data.auto} updates` : " · reports only"}
      </span>
      <span class="spacer" />
      {error && <span class="form-error">{error}</span>}
      <button class="btn btn-ghost btn-small" onClick={check} disabled={checking}>
        <RefreshCw size={13} class={checking ? "spin" : ""} />
        {checking ? "Checking…" : "Check now"}
      </button>
    </div>
  );
}

/**
 * Applies an update to one service: a newer version is committed as a tag
 * bump and deployed; a new image behind the same tag is just pulled and
 * deployed.
 */
export function UpdateDialog(props: { stack: string; update: ServiceUpdate; onClose: () => void }) {
  const u = props.update;
  const options = [u.allowed, u.latest].filter(
    (c, i, all) => c && all.findIndex((x) => x?.tag === c.tag) === i,
  );
  const [tag, setTag] = useState(options[0]?.tag ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const apply = async () => {
    setBusy(true);
    setError("");
    try {
      const job = await api.applyUpdate(props.stack, u.service, tag || undefined);
      navigate({ page: "job", id: job.id });
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <Dialog
      title={`Update ${u.service}`}
      onClose={props.onClose}
      footer={
        <>
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn btn-primary" onClick={apply} disabled={busy}>
            <ArrowUpCircle size={14} /> {tag ? `Update to ${tag}` : "Pull and deploy"}
          </button>
        </>
      }
    >
      <p class="muted mono">{u.image}</p>
      {options.length > 0 ? (
        <>
          <div class="plan">
            {options.map((c) => (
              <label key={c!.tag} class="check">
                <input
                  type="radio"
                  name="tag"
                  checked={tag === c!.tag}
                  onChange={() => setTag(c!.tag)}
                />
                <span class="mono">
                  {tagOf(u.image)} → {c!.tag}
                </span>
                <span class={`chip ${c!.bump === "major" ? "chip-warn" : "chip-accent"}`}>
                  {c!.bump}
                </span>
              </label>
            ))}
          </div>
          <p class="muted">
            Changes the tag in the compose file, commits and pushes it, then deploys {u.service}.
            {options.some((c) => c!.bump === "major") &&
              " A major version may need changes of its own: check its release notes first."}
          </p>
        </>
      ) : (
        <p class="muted">
          The {tagOf(u.image)} tag now points to a newer image. This pulls it and redeploys{" "}
          {u.service}.
        </p>
      )}
      {error && <div class="form-error">{error}</div>}
    </Dialog>
  );
}
