import { RotateCcw } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { api } from "../api";
import { useUnsavedWarning } from "../hooks";
import { messageProblem } from "../lib";
import { navigate } from "../router";
import type { CheckResult, ComposeFile, StackInfo } from "../types";
import { CodeEditor } from "./CodeEditor";
import { Dialog, ErrorNote } from "./ui";

/** An old version picked in History, waiting to be opened in the editor. */
const restores = new Map<string, { content: string; from: string }>();
export function restoreVersion(stack: string, content: string, from: string) {
  restores.set(stack, { content, from });
}

export function ComposeTab(props: { stack: StackInfo; readOnly: boolean; onSaved: () => void }) {
  const name = props.stack.name;
  const [saved, setSaved] = useState<ComposeFile | null>(null);
  const [draft, setDraft] = useState("");
  // Bumped whenever the editor's text is replaced rather than typed.
  const [revision, setRevision] = useState(0);
  const [restoredFrom, setRestoredFrom] = useState("");
  const [error, setError] = useState("");
  const [reviewing, setReviewing] = useState(false);
  const [notice, setNotice] = useState("");

  const load = async () => {
    try {
      const file = await api.compose(name);
      setSaved(file);
      const restore = restores.get(name);
      restores.delete(name);
      setDraft(restore?.content ?? file.content);
      setRevision((r) => r + 1);
      setRestoredFrom(restore?.from ?? "");
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  };
  useEffect(() => {
    load();
  }, [name]);

  const dirty = !!saved && draft !== saved.content;
  useUnsavedWarning(dirty);

  if (!saved)
    return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton editor-skeleton" />;

  return (
    <div class="compose">
      <div class="toolbar">
        <span class="mono muted">
          {props.stack.path}/{props.stack.file}
        </span>
        {restoredFrom && dirty && (
          <span class="chip chip-accent">Restored from {restoredFrom}</span>
        )}
        {dirty && !restoredFrom && <span class="chip chip-accent">Edited</span>}
        <span class="spacer" />
        {notice && !dirty && (
          <span class={notice.includes("failed") ? "tone-bad" : "tone-good"}>{notice}</span>
        )}
        {dirty && (
          <button
            class="btn btn-ghost"
            onClick={() => {
              setDraft(saved.content);
              setRestoredFrom("");
              setRevision((r) => r + 1);
            }}
          >
            <RotateCcw size={14} /> Discard
          </button>
        )}
        {!props.readOnly && (
          <button class="btn btn-primary" disabled={!dirty} onClick={() => setReviewing(true)}>
            Review and save
          </button>
        )}
      </div>
      {error && <ErrorNote>{error}</ErrorNote>}
      {props.stack.git?.modified && !props.readOnly && (
        <p class="note note-warn">
          This is the file as it is on the server, including changes that aren't committed. Saving
          commits them together with your edit; to keep them apart, commit or discard them first
          (Review changes, above).
        </p>
      )}
      <CodeEditor
        value={draft}
        docKey={revision}
        onChange={setDraft}
        readOnly={props.readOnly}
        height="calc(100dvh - 260px)"
      />
      {reviewing && (
        <ReviewDialog
          stack={props.stack}
          saved={saved}
          draft={draft}
          onClose={() => setReviewing(false)}
          onReload={() => {
            setReviewing(false);
            load();
          }}
          onSaved={(file, msg, jobId) => {
            setReviewing(false);
            setSaved(file);
            setRestoredFrom("");
            setNotice(msg);
            props.onSaved();
            if (jobId) navigate({ page: "job", id: jobId });
          }}
        />
      )}
    </div>
  );
}

function ReviewDialog(props: {
  stack: StackInfo;
  saved: ComposeFile;
  draft: string;
  onClose: () => void;
  onReload: () => void;
  onSaved: (file: ComposeFile, notice: string, jobId?: string) => void;
}) {
  const [check, setCheck] = useState<CheckResult | null>(null);
  const [message, setMessage] = useState("");
  const [deploy, setDeploy] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const inGit = !!props.stack.git;
  const problem = inGit && check ? messageProblem(message) : "";

  useEffect(() => {
    api
      .check(props.stack.name, props.draft)
      .then((c) => {
        setCheck(c);
        setMessage(c.message);
      })
      .catch((e: Error) => setError(e.message));
  }, []);

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await api.save(props.stack.name, {
        content: props.draft,
        base: props.saved.hash,
        message,
        deploy,
      });
      let notice = res.commit ? `Saved as ${res.commit}` : "Saved";
      if (res.pushed) notice += " and pushed";
      if (res.push_error)
        notice = `Committed ${res.commit}, but the push failed: ${res.push_error}`;
      props.onSaved({ content: props.draft, hash: res.hash }, notice, res.job?.id);
    } catch (e) {
      const err = e as Error & { status?: number };
      setConflict(err.status === 409);
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <Dialog
      title="Review changes"
      wide
      onClose={props.onClose}
      footer={
        <>
          {!check?.error && (
            <label class="check">
              <input
                type="checkbox"
                checked={deploy}
                onChange={(e) => setDeploy(e.currentTarget.checked)}
              />
              Deploy after saving
            </label>
          )}
          <span class="spacer" />
          <button class="btn btn-ghost" onClick={props.onClose}>
            Keep editing
          </button>
          {conflict ? (
            <button class="btn" onClick={props.onReload}>
              Reload the file
            </button>
          ) : (
            <button
              class="btn btn-primary"
              disabled={!check || !!check.error || busy || !!problem}
              onClick={save}
            >
              {inGit ? "Commit" : "Save"}
              {deploy ? " and deploy" : ""}
            </button>
          )}
        </>
      }
    >
      {!check && !error && <div class="skeleton diff-skeleton" />}
      {check?.error && (
        <div class="note note-bad">
          <strong>docker compose rejects this file:</strong>
          <pre class="mono">{check.error}</pre>
        </div>
      )}
      {check && (
        <>
          <CodeEditor value={props.draft} original={props.saved.content} readOnly height="46dvh" />
          {inGit ? (
            <label class="field">
              <span class="field-label">Commit message</span>
              <input
                class="input"
                value={message}
                onInput={(e) => setMessage(e.currentTarget.value)}
              />
              {problem && <span class="field-hint tone-warn">{problem}</span>}
            </label>
          ) : (
            <p class="muted">This stack isn't in git, so the file is saved without history.</p>
          )}
          {props.saved.crlf && (
            <p class="muted">
              The file has Windows (CRLF) line endings; saving converts it to LF, so git will show
              every line as changed this once.
            </p>
          )}
        </>
      )}
      {error && <div class="form-error">{error}</div>}
    </Dialog>
  );
}
