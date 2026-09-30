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
  /** The compose file had uncommitted changes when this deployed. */
  dirty?: boolean;
  result?: JobResult;
  error?: string;
}

export interface StackInfo {
  name: string;
  path: string;
  file: string;
  project: string;
  self: boolean;
  counts: { services: number; running: number; pending: number; updates: number };
  services: ServiceState[];
  error?: string;
  git?: GitStatus;
  git_error?: string;
  active?: Job;
  last?: Job;
  /** Services the last update check found updates for. */
  updates: ServiceUpdate[];
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

export type Policy = "off" | "digest" | "patch" | "minor";

export interface Candidate {
  tag: string;
  bump: "major" | "minor" | "patch";
}

/** What the update check found for a service. */
export interface ServiceUpdate {
  service: string;
  image: string;
  /** The tag now points to a different image than the running one. */
  new_image?: boolean;
  /** The newest version tag, and the newest the policy would apply. */
  latest?: Candidate;
  allowed?: Candidate;
  policy: Policy;
  skipped?: string;
  error?: string;
}

export interface UpdatesInfo {
  checked_at?: string;
  checking: boolean;
  /** Check interval, e.g. "6h0m0s"; empty when checks only run on demand. */
  every: string;
  auto: Policy;
  count: number;
  stacks: Record<string, ServiceUpdate[]>;
}

/** A compose file changed outside Hoist, next to its last commit. */
export interface DriftInfo {
  committed: ComposeFile;
  current: ComposeFile;
  /** A suggested commit message for the difference. */
  message: string;
}
