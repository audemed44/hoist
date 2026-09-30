import { useEffect, useState } from "preact/hooks";
import { api } from "../api";
import { messageProblem } from "../lib";
import { navigate } from "../router";
import type { DriftInfo } from "../types";
import { CodeEditor } from "./CodeEditor";
import { Dialog, ErrorNote } from "./ui";

/**
 * The compose file was changed on the server, outside Hoist: show how it
 * differs from the last commit, then commit it, throw it away, or take it
 * into the editor.
 */
export function DriftDialog(props: {
  stack: string;
  readOnly: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [drift, setDrift] = useState<DriftInfo | null>(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const problem = messageProblem(message);

  useEffect(() => {
    api
      .drift(props.stack)
      .then((d) => {
        setDrift(d);
        setMessage(d.message);
      })
      .catch((e: Error) => setError(e.message));
  }, [props.stack]);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      props.onDone();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <Dialog
      title="Uncommitted changes"
      wide
      onClose={props.onClose}
      footer={
        props.readOnly ? undefined : confirmDiscard ? (
          <>
            <span class="tone-bad">Throw these changes away and restore the last commit?</span>
            <span class="spacer" />
            <button class="btn btn-ghost" onClick={() => setConfirmDiscard(false)}>
              Keep them
            </button>
            <button
              class="btn btn-danger"
              disabled={busy}
              onClick={() => act(() => api.discard(props.stack))}
            >
              Discard
            </button>
          </>
        ) : (
          <>
            <button class="btn btn-ghost" disabled={busy} onClick={() => setConfirmDiscard(true)}>
              Discard
            </button>
            <button
              class="btn btn-ghost"
              onClick={() => navigate({ page: "stack", name: props.stack, tab: "compose" })}
            >
              Open in the editor
            </button>
            <span class="spacer" />
            <button
              class="btn btn-primary"
              disabled={busy || !drift || !!problem}
              onClick={() => act(() => api.commit(props.stack, message))}
            >
              Commit and push
            </button>
          </>
        )
      }
    >
      <p class="muted">
        The compose file on the server differs from the last commit. Deploys use the file as it is,
        so commit it to keep git in step, or discard it.
      </p>
      {drift ? (
        <CodeEditor
          value={drift.current.content}
          original={drift.committed.content}
          readOnly
          height="46dvh"
        />
      ) : (
        !error && <div class="skeleton diff-skeleton" />
      )}
      {drift && !props.readOnly && (
        <label class="field">
          <span class="field-label">Commit message</span>
          <input class="input" value={message} onInput={(e) => setMessage(e.currentTarget.value)} />
          {problem && <span class="field-hint tone-warn">{problem}</span>}
        </label>
      )}
      {error && <ErrorNote>{error}</ErrorNote>}
    </Dialog>
  );
}
