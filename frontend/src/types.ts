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

/** A host port or container name a service shares with something else. */
export interface Conflict {
  service: string;
  kind: "port" | "container_name";
  /** e.g. 8080/tcp, or the container name. */
  what: string;
  /** Who else has it. */
  with: string;
}

/** A service to add for an image, as the server suggests it. */
export interface ServiceSuggestion {
  service: string;
  image: string;
  /** Recent version tags, newest first. */
  tags: string[];
  ports: { container: number; protocol: string; host: number }[];
  volumes: { container: string; host: string }[];
  /** The image defines a healthcheck. */
  healthcheck: boolean;
  note?: string;
}

/** Advice about a compose file. */
export interface Hint {
  service: string;
  kind: "latest" | "restart" | "healthcheck" | "secret";
  message: string;
  /** The saved file doesn't have this problem; the edit brings it. */
  new: boolean;
}

export interface CheckResult {
  message: string;
  error?: string;
  conflicts: Conflict[];
  hints: Hint[];
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

/** A compose project running on this host that no stack covers yet. */
export interface ProjectCandidate {
  /** The compose project name. */
  name: string;
  dir: string;
  files: string[];
  services: string[];
  running: number;
  total: number;
  /** Suggested stack name, folder and compose file. */
  stack: string;
  path: string;
  file: string;
  /** Why it can't be adopted as it is. */
  problem?: string;
}

export interface Discovery {
  projects: ProjectCandidate[];
  /** Folders the stacks live in, where a new one would go. */
  parents: string[];
}

export interface AddStack {
  name: string;
  path: string;
  project?: string;
  file?: string;
  create?: boolean;
  content?: string;
  message?: string;
  deploy?: boolean;
}

export interface AddResult {
  stack: string;
  commit?: string;
  pushed: boolean;
  push_error?: string;
  git_note?: string;
  job?: Job;
}

/** One entry of the audit log. */
export interface AuditEvent {
  id: number;
  time: string;
  stack: string;
  /** Services the action was limited to; empty for the whole stack. */
  services: string[];
  action: string;
  /** ui, api, foyer or auto. */
  trigger: string;
  detail?: string;
  commit?: string;
  job?: string;
  result: "ok" | "failed" | "running";
  error?: string;
}

/** A compose file changed outside Hoist, next to its last commit. */
export interface DriftInfo {
  committed: ComposeFile;
  current: ComposeFile;
  /** A suggested commit message for the difference. */
  message: string;
}
