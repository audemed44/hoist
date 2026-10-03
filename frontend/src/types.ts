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
  /** Stopped on purpose by Gatehouse's scale-to-zero, e.g. "sleeping". */
  asleep?: string;
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
  /** The earlier deploy this one rolled back to. */
  rollback?: string;
  /** Ran with images pinned by digest (the stack was rolled back). */
  pinned?: boolean;
  result?: JobResult;
  error?: string;
}

/** What one container of a stack ran after a deploy. */
export interface DeployedImage {
  service: string;
  container: string;
  container_id: string;
  /** The image as compose was given it, e.g. ghcr.io/a/b:latest. */
  ref: string;
  image_id: string;
  /** Registry digest; empty for images built locally. */
  digest?: string;
  /** OCI labels: the repository it was built from, and the commit. */
  source?: string;
  revision?: string;
  state: string;
}

/** A finished deploy and what it left running. */
export interface DeployRecord {
  job: string;
  time: string;
  finished: string;
  stack: string;
  services: string[];
  /** ui, api, foyer, auto, releases, or baseline (recorded when Hoist first saw the stack). */
  trigger: string;
  commit?: string;
  dirty?: boolean;
  result: "ok" | "failed";
  rollback?: string;
  pinned?: boolean;
  /** When it had run long enough without trouble to roll back to. */
  good_at?: string;
  images: DeployedImage[];
  /** The deploy the stack runs now. */
  current?: boolean;
}

export interface RollbackService {
  service: string;
  from?: DeployedImage;
  to: DeployedImage;
  /** The image@digest it'll run; empty when it can't be pinned. */
  pinned?: string;
  changes: boolean;
  /** Where the image comes from: on this host, or pulled from the registry. */
  where?: "host" | "registry";
  problem?: string;
}

export interface RollbackPlan {
  target: DeployRecord;
  services: RollbackService[];
  /** The older compose file, when it differs from the current one. */
  compose?: ComposeFile;
  current?: ComposeFile;
  notes: string[];
  /** Why it can't be done. */
  problem?: string;
}

/** A stack rolled back to an earlier deploy, its images pinned by digest. */
export interface PinInfo {
  deploy: string;
  at: string;
  commit?: string;
  images: Record<string, string>;
  /** Runs the older deploy's compose file, not the current one. */
  old_compose?: boolean;
}

export interface StackInfo {
  name: string;
  path: string;
  file: string;
  project: string;
  self: boolean;
  counts: { services: number; running: number; pending: number; updates: number; asleep: number };
  services: ServiceState[];
  error?: string;
  git?: GitStatus;
  git_error?: string;
  active?: Job;
  last?: Job;
  /** Services the last update check found updates for. */
  updates: ServiceUpdate[];
  /** Set while the stack is rolled back. */
  pin?: PinInfo;
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
  /** Foyer, the homelab's start page (HOMEPAGE_URL). */
  foyer_url?: string;
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

export interface GitHubCommit {
  sha: string;
  /** The first line. */
  message: string;
  author: string;
  time: string;
  url: string;
}

export interface WorkflowRun {
  id: number;
  name: string;
  workflow: number;
  path: string;
  event: string;
  branch: string;
  sha: string;
  attempt: number;
  /** queued, in_progress, completed… */
  status: string;
  /** Once completed: success, failure, cancelled, skipped… */
  conclusion?: string;
  url: string;
  created: string;
  updated: string;
}

export type PRState =
  | "draft"
  | "conflict"
  | "ci-failing"
  | "ci-running"
  | "checking"
  | "blocked"
  | "changes-requested"
  | "ready";

export interface PullRequest {
  number: number;
  title: string;
  draft: boolean;
  author: string;
  created: string;
  url: string;
  branch: string;
  sha: string;
  same_repo: boolean;
  mergeable?: boolean;
  rebaseable?: boolean;
  mergeable_state?: string;
  review?: "approved" | "changes_requested";
  ci: "passing" | "failing" | "pending" | "none";
  runs: WorkflowRun[];
  state: PRState;
}

export type AppState = "build-failed" | "ready" | "building" | "not-built" | "deployed";

/** One of your apps on the release board: its branch, image and what runs. */
export interface ReleaseApp {
  id: string;
  /** owner/name */
  repo: string;
  url: string;
  stack: string;
  services: string[];
  image: string;
  running: { revision?: string; digest?: string; since: string };
  branch: string;
  head: string;
  /** Commits on the branch the running image doesn't have. */
  behind: number;
  diverged?: boolean;
  /** The newest of them, newest first. */
  commits: GitHubCommit[];
  compare_url?: string;
  /** The latest image build on the branch. */
  build?: WorkflowRun;
  /** The registry digest of the image's tag now. */
  latest?: string;
  state: AppState;
  prs: PullRequest[];
  errors: string[];
  /** Its stack is rolled back. */
  pinned?: boolean;
  active?: Job;
  last?: Job;
  /** Merges waiting to deploy, and recent ones. */
  ships: Ship[];
}

/** "Merge and deploy when ready": a merge followed through to a deploy. */
export interface Ship {
  id: string;
  repo: string;
  stack: string;
  number: number;
  title: string;
  sha: string;
  state: "building" | "deploying" | "done" | "failed" | "cancelled";
  message: string;
  build_url?: string;
  job?: string;
  started: string;
  finished?: string;
}

export interface ReleasesInfo {
  /** False without HOIST_GITHUB_TOKEN. */
  configured: boolean;
  checked_at: string;
  owners: string[];
  error?: string;
  /** The image build workflow, e.g. docker.yml. */
  workflow: string;
  apps: ReleaseApp[];
  summary: { prs: number; ready: number; failing: number; to_merge: number };
}
