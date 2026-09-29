import { Eye, EyeOff, Plus, Trash2 } from "lucide-preact";
import { useEffect, useState } from "preact/hooks";
import { api } from "../api";
import { useUnsavedWarning } from "../hooks";
import type { EnvChange, EnvInfo, StackInfo } from "../types";
import { ErrorNote } from "./ui";

/**
 * A row being edited. `value` is null while the value stays on the server
 * (unrevealed and untouched); `original` is what was revealed, to tell an
 * edit from a peek.
 */
interface Row {
  id: number;
  key: string;
  isNew: boolean;
  set: boolean;
  value: string | null;
  original: string | null;
  shown: boolean;
}

let nextId = 1;

function rowsFrom(info: EnvInfo): Row[] {
  return info.entries.map((e) => ({
    id: nextId++,
    key: e.key,
    isNew: false,
    set: e.set,
    value: null,
    original: null,
    shown: false,
  }));
}

export function EnvTab(props: { stack: StackInfo; readOnly: boolean }) {
  const name = props.stack.name;
  const [info, setInfo] = useState<EnvInfo | null>(null);
  const [rows, setRows] = useState<Row[]>([]);
  const [removed, setRemoved] = useState(0);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState("");

  const reset = (i: EnvInfo) => {
    setInfo(i);
    setRows(rowsFrom(i));
    setRemoved(0);
  };
  useEffect(() => {
    api
      .env(name)
      .then(reset)
      .catch((e: Error) => setError(e.message));
  }, [name]);

  const edited = rows.some((r) => r.isNew || (r.value !== null && r.value !== r.original));
  const dirty = edited || removed > 0;
  useUnsavedWarning(dirty);

  if (!info)
    return error ? <ErrorNote>{error}</ErrorNote> : <div class="skeleton editor-skeleton" />;

  const update = (id: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)));

  const reveal = async (row: Row) => {
    if (row.value !== null) {
      update(row.id, { shown: !row.shown });
      return;
    }
    try {
      const { value } = await api.envValue(name, row.key);
      update(row.id, { value, original: value, shown: true });
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const add = (key = "") => {
    setRows((rs) => [
      ...rs,
      { id: nextId++, key, isNew: true, set: false, value: "", original: null, shown: true },
    ]);
    setNotice("");
  };

  const save = async () => {
    setSaving(true);
    setError("");
    const entries: EnvChange[] = rows
      .filter((r) => r.key.trim())
      .map((r) => ({
        key: r.key.trim(),
        value: r.isNew || (r.value !== null && r.value !== r.original) ? (r.value ?? "") : null,
      }));
    try {
      reset(await api.saveEnv(name, entries));
      setNotice("Saved. Deploy to apply it to the containers.");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const keys = new Set(rows.map((r) => r.key));
  const missing = info.missing.filter((k) => !keys.has(k));

  return (
    <div class="env">
      <p class="muted env-intro">
        <code>{props.stack.path}/.env</code>, which compose reads to fill in{" "}
        <code>${"{VARS}"}</code> in the compose file. It stays out of git. Values are written
        exactly as typed, quotes included.
      </p>
      {missing.length > 0 && (
        <div class="note note-bad">
          The compose file uses {missing.length === 1 ? "a variable" : "variables"} that{" "}
          {missing.length === 1 ? "isn't" : "aren't"} set:{" "}
          {missing.map((k) => (
            <button
              key={k}
              class="chip chip-button"
              disabled={props.readOnly}
              onClick={() => add(k)}
            >
              <Plus size={11} /> {k}
            </button>
          ))}
        </div>
      )}
      {!info.exists && (
        <p class="note">
          There's no .env file yet; saving creates one (readable only by its owner).
        </p>
      )}
      <div class="env-list">
        {rows.map((r) => {
          const unused = !r.isNew && info.unused.includes(r.key);
          return (
            <div key={r.id} class="env-row">
              {r.isNew ? (
                <input
                  class="input code"
                  placeholder="NAME"
                  value={r.key}
                  autofocus={!r.key}
                  spellcheck={false}
                  onInput={(e) => update(r.id, { key: e.currentTarget.value })}
                />
              ) : (
                <span
                  class="env-key mono"
                  title={unused ? "Not used by the compose file" : undefined}
                >
                  {r.key}
                  {unused && <span class="chip">unused</span>}
                </span>
              )}
              {r.shown ? (
                <input
                  class="input code"
                  value={r.value ?? ""}
                  spellcheck={false}
                  readOnly={props.readOnly}
                  onInput={(e) => update(r.id, { value: e.currentTarget.value })}
                />
              ) : (
                <button
                  class="env-hidden"
                  onClick={() => reveal(r)}
                  title="Show the value"
                  disabled={!r.set && r.value === null && props.readOnly}
                >
                  {r.set || r.value ? "••••••••••••" : <span class="muted">empty</span>}
                </button>
              )}
              <span class="env-actions">
                {!r.isNew && (
                  <button
                    class="icon-btn"
                    onClick={() => reveal(r)}
                    aria-label={r.shown ? "Hide value" : "Show value"}
                  >
                    {r.shown ? <EyeOff size={15} /> : <Eye size={15} />}
                  </button>
                )}
                {!props.readOnly && (
                  <button
                    class="icon-btn"
                    aria-label={`Remove ${r.key}`}
                    onClick={() => {
                      setRows((rs) => rs.filter((x) => x.id !== r.id));
                      if (!r.isNew) setRemoved((n) => n + 1);
                    }}
                  >
                    <Trash2 size={15} />
                  </button>
                )}
              </span>
            </div>
          );
        })}
      </div>
      {!props.readOnly && (
        <div class="toolbar">
          <button class="btn btn-ghost" onClick={() => add()}>
            <Plus size={14} /> Add variable
          </button>
          <span class="spacer" />
          {error && <span class="form-error">{error}</span>}
          {notice && !dirty && <span class="tone-good">{notice}</span>}
          {dirty && (
            <button class="btn btn-ghost" onClick={() => reset(info)}>
              Discard
            </button>
          )}
          <button class="btn btn-primary" disabled={!dirty || saving} onClick={save}>
            Save .env
          </button>
        </div>
      )}
    </div>
  );
}
