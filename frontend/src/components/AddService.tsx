import { useState } from "preact/hooks";
import { api } from "../api";
import { insertService } from "../lib";
import type { ServiceSuggestion } from "../types";
import { Dialog, Field } from "./ui";

/**
 * Looks an image up in its registry (no pull) and inserts a service for it
 * into the editor: a pinned tag, free host ports, and folders next to the
 * compose file. Nothing is saved; the edit goes through review as usual.
 */
export function AddServiceDialog(props: {
  stack: string;
  draft: string;
  onInsert: (content: string) => void;
  onClose: () => void;
}) {
  const [image, setImage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [s, setS] = useState<ServiceSuggestion | null>(null);

  const lookUp = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      setS(await api.suggestService(props.stack, image.trim(), props.draft));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const repo = s ? s.image.replace(/(@.*|:[^/:]*)$/, "") : "";
  const insert = () => {
    if (!s) return;
    props.onInsert(
      insertService(props.draft, {
        name: s.service,
        image: s.image,
        ports: s.ports,
        volumes: s.volumes,
      }),
    );
  };

  return (
    <Dialog
      title="Add a service"
      wide
      onClose={props.onClose}
      footer={
        <>
          <span class="muted">Inserted into the editor; review and save as usual.</span>
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn btn-primary" disabled={!s || !s.service} onClick={insert}>
            Insert
          </button>
        </>
      }
    >
      <form class="lookup" onSubmit={lookUp}>
        <Field
          label="Image"
          hint="From Docker Hub, ghcr.io or another public registry. Nothing is pulled."
        >
          <div class="lookup-row">
            <input
              class="input mono"
              value={image}
              placeholder="ghcr.io/rommapp/romm"
              onInput={(e) => setImage(e.currentTarget.value)}
              autoFocus
            />
            <button class="btn" disabled={busy || !image.trim()}>
              {busy ? "Looking up…" : "Look up"}
            </button>
          </div>
        </Field>
      </form>
      {error && <div class="form-error">{error}</div>}
      {s && (
        <div class="suggestion">
          {s.note && <div class="note note-warn">{s.note}</div>}
          <div class="suggest-fields">
            <Field label="Service">
              <input
                class="input mono"
                value={s.service}
                onInput={(e) => {
                  const name = e.currentTarget.value;
                  setS({
                    ...s,
                    service: name,
                    volumes: s.volumes.map((v) => ({
                      ...v,
                      host: v.host.replace(/^\.\/[^/]*\//, `./${name}/`),
                    })),
                  });
                }}
              />
            </Field>
            <Field
              label="Version"
              hint={
                s.image.endsWith(":latest")
                  ? "No version tags found; it follows :latest."
                  : undefined
              }
            >
              {s.tags.length > 0 ? (
                <select
                  class="input mono"
                  value={s.image.slice(repo.length + 1)}
                  onChange={(e) => setS({ ...s, image: `${repo}:${e.currentTarget.value}` })}
                >
                  {!s.tags.includes(s.image.slice(repo.length + 1)) && (
                    <option value={s.image.slice(repo.length + 1)}>
                      {s.image.slice(repo.length + 1)}
                    </option>
                  )}
                  {s.tags.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  class="input mono"
                  value={s.image}
                  onInput={(e) => setS({ ...s, image: e.currentTarget.value })}
                />
              )}
            </Field>
          </div>
          {s.ports.length > 0 && (
            <div class="mapping">
              <span class="field-label">Ports · host → container</span>
              {s.ports.map((p, i) => (
                <div key={`${p.container}/${p.protocol}`} class="mapping-row">
                  <input
                    class="input mono"
                    type="number"
                    min={1}
                    max={65535}
                    value={p.host}
                    aria-label={`Host port for ${p.container}/${p.protocol}`}
                    onInput={(e) => {
                      const ports = [...s.ports];
                      ports[i] = { ...p, host: Number(e.currentTarget.value) };
                      setS({ ...s, ports });
                    }}
                  />
                  <span class="mono muted">
                    → {p.container}/{p.protocol}
                  </span>
                  <button
                    type="button"
                    class="link-btn"
                    onClick={() => setS({ ...s, ports: s.ports.filter((_, j) => j !== i) })}
                  >
                    Remove
                  </button>
                </div>
              ))}
              <span class="field-hint">
                Host ports are free on this server as far as Hoist can tell.
              </span>
            </div>
          )}
          {s.volumes.length > 0 && (
            <div class="mapping">
              <span class="field-label">Volumes · host → container</span>
              {s.volumes.map((v, i) => (
                <div key={v.container} class="mapping-row">
                  <input
                    class="input mono"
                    value={v.host}
                    aria-label={`Host folder for ${v.container}`}
                    onInput={(e) => {
                      const volumes = [...s.volumes];
                      volumes[i] = { ...v, host: e.currentTarget.value };
                      setS({ ...s, volumes });
                    }}
                  />
                  <span class="mono muted">→ {v.container}</span>
                  <button
                    type="button"
                    class="link-btn"
                    onClick={() => setS({ ...s, volumes: s.volumes.filter((_, j) => j !== i) })}
                  >
                    Remove
                  </button>
                </div>
              ))}
            </div>
          )}
          {!s.ports.length && !s.volumes.length && (
            <p class="muted">
              The image declares no ports or volumes; add what it needs after inserting.
            </p>
          )}
          <p class="muted">
            {s.healthcheck
              ? "The image has its own healthcheck."
              : "The image has no healthcheck; consider adding one after inserting."}
          </p>
        </div>
      )}
    </Dialog>
  );
}
