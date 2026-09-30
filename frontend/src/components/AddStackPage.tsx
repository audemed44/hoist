import { useState } from "preact/hooks";
import { api } from "../api";
import { useData } from "../hooks";
import { messageProblem, projectName, stackName } from "../lib";
import { navigate } from "../router";
import type { AddResult, ProjectCandidate } from "../types";
import { CodeEditor } from "./CodeEditor";
import { ErrorNote, Field, SectionHead } from "./ui";

const TEMPLATE = `services:
  app:
    image: nginx:1.27-alpine
    restart: unless-stopped
    ports:
      - "8080:80"
    volumes:
      - ./data:/data
`;

/** Adopt a compose project already running here, or create a new stack. */
export function AddStackPage(props: { readOnly: boolean }) {
  const { data, error, reload } = useData(api.discover, 0);
  const [done, setDone] = useState<AddResult | null>(null);

  const finished = (res: AddResult) => {
    if (res.git_note) {
      setDone(res);
      window.scrollTo(0, 0);
    } else if (res.job) navigate({ page: "job", id: res.job.id });
    else navigate({ page: "stack", name: res.stack, tab: "services" });
  };

  return (
    <div class="page">
      <header class="page-head">
        <div class="eyebrow">
          <a class="link" href="/">
            Stacks
          </a>
          <span>/</span>
          <span>New</span>
        </div>
        <h1 class="page-title">Add a stack</h1>
        <p class="muted page-lede">
          Adopt a compose project that's already running on this server, or start a new one. Either
          way it's added to hoist.yaml with the right project name, so compose recognises its
          containers.
        </p>
        {props.readOnly && (
          <div class="note note-warn">Hoist is read-only, so stacks can't be added.</div>
        )}
        {done && (
          <div class="note note-warn">
            <strong>{done.stack}</strong> was added
            {done.commit ? ` and committed as ${done.commit}` : ""}. {done.git_note}{" "}
            <a class="link-btn" href={done.job ? `/jobs/${done.job.id}` : `/stacks/${done.stack}`}>
              {done.job ? "Follow the deploy →" : "Open it →"}
            </a>
          </div>
        )}
      </header>

      <section class="add-section">
        <SectionHead index={1} title="Running on this server" />
        {error && <ErrorNote>{error}</ErrorNote>}
        {!data && !error && <div class="skeleton list-skeleton" />}
        {data && !data.projects.length && (
          <div class="empty">Every compose project running here is already a stack.</div>
        )}
        {data && data.projects.length > 0 && (
          <div class="list">
            {data.projects.map((c) => (
              <AdoptRow
                key={c.name}
                candidate={c}
                readOnly={props.readOnly}
                onDone={(res) => {
                  reload();
                  finished(res);
                }}
              />
            ))}
          </div>
        )}
      </section>

      <section class="add-section">
        <SectionHead index={2} title="New stack" />
        {data && <CreateForm parents={data.parents} readOnly={props.readOnly} onDone={finished} />}
      </section>
    </div>
  );
}

function AdoptRow(props: {
  candidate: ProjectCandidate;
  readOnly: boolean;
  onDone: (res: AddResult) => void;
}) {
  const c = props.candidate;
  const [name, setName] = useState(c.stack);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const adopt = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      props.onDone(await api.addStack({ name, path: c.path, project: c.name, file: c.file }));
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  return (
    <form class="list-row adopt-row" onSubmit={adopt}>
      <span class="list-main">
        <span class="list-title">
          <span class="mono">{c.name}</span>
          <span class="muted">
            {" "}
            · {c.running}/{c.total} running
          </span>
        </span>
        <span class="list-sub mono">{c.files.join(", ") || c.dir || "unknown folder"}</span>
        <span class="list-sub">{c.services.join(", ")}</span>
        {c.problem && <span class="audit-detail tone-warn">{c.problem}</span>}
        {error && <span class="form-error">{error}</span>}
      </span>
      {!c.problem && !props.readOnly && (
        <span class="adopt-actions">
          <input
            class="input"
            aria-label={`Stack name for ${c.name}`}
            value={name}
            onInput={(e) => setName(e.currentTarget.value)}
          />
          <button class="btn btn-primary" disabled={busy || !name}>
            {busy ? "Adopting…" : "Adopt"}
          </button>
        </span>
      )}
    </form>
  );
}

function CreateForm(props: {
  parents: string[];
  readOnly: boolean;
  onDone: (res: AddResult) => void;
}) {
  const [name, setName] = useState("");
  const [parent, setParent] = useState(props.parents[0] ?? "");
  const [folder, setFolder] = useState<string | null>(null); // null: follow the name
  const [project, setProject] = useState("");
  const [content, setContent] = useState(TEMPLATE);
  const [message, setMessage] = useState<string | null>(null);
  const [deploy, setDeploy] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const cleanName = stackName(name);
  const path = folder ?? (parent && cleanName ? `${parent.replace(/\/$/, "")}/${cleanName}` : "");
  const commitMessage = message ?? (cleanName ? `feat(${cleanName}): add stack` : "");
  const problem = commitMessage ? messageProblem(commitMessage) : "";

  const create = async (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      props.onDone(
        await api.addStack({
          name: cleanName,
          path,
          project: project.trim() || undefined,
          create: true,
          content,
          message: commitMessage,
          deploy,
        }),
      );
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form class="create-form" onSubmit={create}>
      <div class="create-fields">
        <Field label="Name" hint={name && name !== cleanName ? `Will be ${cleanName}` : undefined}>
          <input
            class="input"
            value={name}
            placeholder="romm"
            onInput={(e) => setName(e.currentTarget.value)}
            disabled={props.readOnly}
          />
        </Field>
        <Field
          label="Folder"
          hint="On the host. Hoist creates it; its parent must exist and be mounted into Hoist."
        >
          <input
            class="input mono"
            value={path}
            placeholder={parent ? `${parent}/…` : "/home/you/homelab/romm"}
            onInput={(e) => setFolder(e.currentTarget.value)}
            disabled={props.readOnly}
          />
        </Field>
        <Field label="Compose project" hint="Defaults to the folder name, as in compose.">
          <input
            class="input mono"
            value={project}
            placeholder={path ? projectName(path) : ""}
            onInput={(e) => setProject(e.currentTarget.value)}
            disabled={props.readOnly}
          />
        </Field>
      </div>
      {props.parents.length > 1 && folder === null && (
        <div class="toolbar">
          <span class="muted">Put it next to</span>
          {props.parents.map((p) => (
            <button
              key={p}
              type="button"
              class={`chip ${p === parent ? "chip-accent" : ""}`}
              onClick={() => setParent(p)}
            >
              {p}
            </button>
          ))}
        </div>
      )}
      <Field label="compose.yml">
        <CodeEditor
          value={content}
          onChange={setContent}
          readOnly={props.readOnly}
          height="360px"
        />
      </Field>
      <Field label="Commit message" hint={problem || "Used when the folder is in a git repo."}>
        <input
          class="input"
          value={commitMessage}
          onInput={(e) => setMessage(e.currentTarget.value)}
          disabled={props.readOnly}
        />
      </Field>
      {error && <ErrorNote>{error}</ErrorNote>}
      {!props.readOnly && (
        <div class="toolbar">
          <label class="check">
            <input
              type="checkbox"
              checked={deploy}
              onChange={(e) => setDeploy(e.currentTarget.checked)}
            />
            Deploy after creating
          </label>
          <span class="spacer" />
          <button class="btn btn-primary" disabled={busy || !cleanName || !path || !!problem}>
            {busy ? "Creating…" : deploy ? "Create and deploy" : "Create stack"}
          </button>
        </div>
      )}
    </form>
  );
}
