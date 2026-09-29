import type {
  CheckResult,
  Commit,
  ComposeFile,
  EnvChange,
  EnvInfo,
  GitStatus,
  Job,
  SaveResult,
  Session,
  StackInfo,
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

const stack = (name: string) => `/api/stacks/${encodeURIComponent(name)}`;

export const api = {
  session: () => request<Session>("/api/session"),
  login: (token: string) => request<Session>("/api/session", json("POST", { token })),
  logout: () => request<void>("/api/session", { method: "DELETE" }),

  stacks: () => request<StackInfo[]>("/api/stacks"),
  stack: (name: string) => request<StackInfo>(stack(name)),
  compose: (name: string) => request<ComposeFile>(`${stack(name)}/compose`),
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
  commit: (name: string, message: string) =>
    request<GitStatus>(`${stack(name)}/git/commit`, json("POST", { message })),

  deploy: (name: string, services?: string[]) =>
    request<Job>(`${stack(name)}/deploy`, json("POST", { services: services ?? [] })),
  jobs: (stackName?: string, limit = 30) =>
    request<Job[]>(
      `/api/jobs?limit=${limit}${stackName ? `&stack=${encodeURIComponent(stackName)}` : ""}`,
    ),
  job: (id: string) => request<Job>(`/api/jobs/${id}`),
  jobLogUrl: (id: string) => `/api/jobs/${id}/log`,
};
