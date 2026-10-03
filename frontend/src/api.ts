import type {
  AddResult,
  AddStack,
  AuditEvent,
  CheckResult,
  Commit,
  ComposeFile,
  Discovery,
  DeployRecord,
  DriftInfo,
  ServiceSuggestion,
  EnvChange,
  EnvInfo,
  GitStatus,
  Job,
  ReleasesInfo,
  RollbackPlan,
  SaveResult,
  Ship,
  Session,
  StackInfo,
  UpdatesInfo,
} from "./types";

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

/** Called when the session has expired, so the app can show the sign-in. */
let onUnauthorized = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin", ...init });
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      message = (await res.json()).error ?? message;
    } catch {
      // not JSON
    }
    if (res.status === 401 && !path.startsWith("/api/session")) onUnauthorized();
    throw new ApiError(message, res.status);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

const json = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});

export interface AuditQuery {
  stack?: string;
  /** An action, or a group ending in "." such as "git.". */
  action?: string;
  trigger?: string;
  result?: string;
  before?: number;
  limit?: number;
}

function queryString(q: object): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") params.set(k, String(v));
  const s = params.toString();
  return s ? `?${s}` : "";
}

const stack = (name: string) => `/api/stacks/${encodeURIComponent(name)}`;

export const api = {
  session: () => request<Session>("/api/session"),
  login: (token: string) => request<Session>("/api/session", json("POST", { token })),
  logout: () => request<void>("/api/session", { method: "DELETE" }),

  stacks: () => request<StackInfo[]>("/api/stacks"),
  stackNames: () => request<string[]>("/api/stack-names"),
  discover: () => request<Discovery>("/api/discover"),
  addStack: (body: AddStack) => request<AddResult>("/api/stacks", json("POST", body)),
  stack: (name: string) => request<StackInfo>(stack(name)),
  compose: (name: string) => request<ComposeFile>(`${stack(name)}/compose`),
  suggestService: (name: string, image: string, content: string) =>
    request<ServiceSuggestion>(`${stack(name)}/suggest-service`, json("POST", { image, content })),
  check: (name: string, content: string) =>
    request<CheckResult>(`${stack(name)}/check`, json("POST", { content })),
  save: (name: string, body: { content: string; base: string; message: string; deploy: boolean }) =>
    request<SaveResult>(`${stack(name)}/compose`, json("PUT", body)),

  env: (name: string) => request<EnvInfo>(`${stack(name)}/env`),
  envValue: (name: string, key: string) =>
    request<{ value: string }>(`${stack(name)}/env/${encodeURIComponent(key)}`),
  saveEnv: (name: string, entries: EnvChange[]) =>
    request<EnvInfo>(`${stack(name)}/env`, json("PUT", { entries })),

  history: (name: string) => request<Commit[]>(`${stack(name)}/history`),
  historyFile: (name: string, commit: Commit) =>
    request<ComposeFile>(
      `${stack(name)}/history/${commit.hash}?path=${encodeURIComponent(commit.path)}`,
    ),

  git: (name: string, action: "fetch" | "pull" | "push") =>
    request<GitStatus>(`${stack(name)}/git/${action}`, { method: "POST" }),
  drift: (name: string) => request<DriftInfo>(`${stack(name)}/drift`),
  discard: (name: string) => request<GitStatus>(`${stack(name)}/git/discard`, { method: "POST" }),
  commit: (name: string, message: string) =>
    request<GitStatus>(`${stack(name)}/git/commit`, json("POST", { message })),

  deploy: (name: string, services?: string[]) =>
    request<Job>(`${stack(name)}/deploy`, json("POST", { services: services ?? [] })),
  /** Finished deploys and what they ran, newest first. */
  deploys: (name: string) => request<DeployRecord[]>(`${stack(name)}/deploys`),
  /** A rollback to the deploy `to`, or by default to the last good one before the current. */
  rollbackPlan: (name: string, to?: string) =>
    request<RollbackPlan>(`${stack(name)}/rollback${to ? `?to=${encodeURIComponent(to)}` : ""}`),
  rollback: (name: string, to: string) =>
    request<Job>(`${stack(name)}/rollback`, json("POST", { to })),
  /** Unpins a rolled-back stack and deploys its compose file's images again. */
  resume: (name: string) => request<Job>(`${stack(name)}/resume`, { method: "POST" }),
  updates: () => request<UpdatesInfo>("/api/updates"),
  checkUpdates: () => request<void>("/api/updates/check", { method: "POST" }),
  /** Bumps a service to tag (commit and deploy), or without a tag pulls its new image. */
  applyUpdate: (stackName: string, service: string, tag?: string) =>
    request<Job>(
      `${stack(stackName)}/services/${encodeURIComponent(service)}/update`,
      json("POST", { tag: tag ?? "" }),
    ),
  /** The release board; refresh asks GitHub again instead of using the last few minutes' answer. */
  releases: (refresh = false) =>
    request<ReleasesInfo>(`/api/releases${refresh ? "?refresh=1" : ""}`),
  /** Rebase-merges a pull request and deletes its branch; force skips the green-checks rule. */
  merge: (repo: string, stackName: string, number: number, force = false) =>
    request<{ sha: string; deleted: boolean; delete_error?: string }>(
      "/api/releases/merge",
      json("POST", { repo, stack: stackName, number, force }),
    ),
  /** Merges, waits for the image build, then deploys the app. */
  ship: (repo: string, stackName: string, number: number, force = false) =>
    request<Ship>("/api/releases/ship", json("POST", { repo, stack: stackName, number, force })),
  /** Cancels a ship that's waiting for its build, or dismisses a finished one. */
  cancelShip: (id: string) =>
    request<void>(`/api/releases/ships/${encodeURIComponent(id)}`, { method: "DELETE" }),
  /** Re-runs a pull request's failed checks, or (number 0) the failed image build. */
  rerun: (repo: string, stackName: string, number = 0) =>
    request<void>("/api/releases/rerun", json("POST", { repo, stack: stackName, number })),
  /** Pulls and redeploys the services that run an app. */
  deployApp: (repo: string, stackName: string) =>
    request<Job>("/api/releases/deploy", json("POST", { repo, stack: stackName })),
  jobs: (stackName?: string, limit = 30) =>
    request<Job[]>(
      `/api/jobs?limit=${limit}${stackName ? `&stack=${encodeURIComponent(stackName)}` : ""}`,
    ),
  job: (id: string) => request<Job>(`/api/jobs/${id}`),
  audit: (q: AuditQuery) => request<AuditEvent[]>(`/api/audit${queryString(q)}`),
  jobLogUrl: (id: string) => `/api/jobs/${id}/log`,
};
