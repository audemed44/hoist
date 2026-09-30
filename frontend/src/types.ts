export type Change = "" | "create" | "recreate" | "start" | "remove";

export interface Container {
  id: string;
  name: string;
  service: string;
  image: string;
  state: string;
  status: string;
  created: number;
}

export interface ServiceState {
  name: string;
  image: string;
  change?: Change;
  container?: Container;
  orphan?: boolean;
}

export interface GitStatus {
  branch: string;
  head: string;
  upstream?: string;
  ahead: number;
  behind: number;
  modified: boolean;
  untracked: boolean;
  fetched_at?: string;
  fetch_error?: string;
}

export type JobState = "running" | "done" | "failed";

export interface JobResult {
  created: string[];
  recreated: string[];
  removed: string[];
  started: string[];
  updated: string[];
}

export interface Job {
  id: string;
  stack: string;
  services?: string[];
  state: JobState;
  started: string;
  finished?: string;
  trigger: string;
  self?: boolean;
  commit?: string;
  result?: JobResult;
  error?: string;
}

export interface StackInfo {
  name: string;
  path: string;
  file: string;
  project: string;
  self: boolean;
  counts: { services: number; running: number; pending: number };
  services: ServiceState[];
  error?: string;
  git?: GitStatus;
  git_error?: string;
  active?: Job;
  last?: Job;
}

export interface ComposeFile {
  content: string;
  hash: string;
  /** Stored with Windows line endings; saving converts it to LF. */
  crlf?: boolean;
}

export interface CheckResult {
  message: string;
  error?: string;
}

export interface SaveResult {
  hash: string;
  commit?: string;
  pushed: boolean;
  push_error?: string;
  job?: Job;
}

export interface EnvInfo {
  exists: boolean;
  entries: { key: string; set: boolean }[];
  missing: string[];
  unused: string[];
}

/** A .env edit: null keeps the current value. */
export interface EnvChange {
  key: string;
  value: string | null;
}

export interface Commit {
  hash: string;
  short: string;
  author: string;
  time: string;
  subject: string;
  path: string;
}

export interface Session {
  authenticated: boolean;
  read_only: boolean;
  /** Commit messages must follow Conventional Commits. */
  conventional: boolean;
}
