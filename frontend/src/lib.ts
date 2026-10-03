import type {
  AuditEvent,
  Change,
  GitStatus,
  Job,
  JobResult,
  ServiceUpdate,
  StackInfo,
} from "./types";

/** Server settings the whole UI needs; filled in from the session. */
export const settings = { conventional: true };

export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const s = Math.max(0, (now - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 48 * 3600) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function duration(job: Job): string {
  if (!job.finished) return "";
  const s = Math.round((new Date(job.finished).getTime() - new Date(job.started).getTime()) / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

export const CHANGE_LABEL: Record<Exclude<Change, "">, string> = {
  create: "Will create",
  recreate: "Will recreate",
  start: "Will start",
  remove: "Will remove",
};

/** One line for what a deploy did, e.g. "Recreated foyer, shelfloom". */
export function resultSummary(r: JobResult | undefined): string {
  if (!r) return "";
  const parts: string[] = [];
  const add = (verb: string, names: string[]) => {
    if (names.length) parts.push(`${verb} ${names.join(", ")}`);
  };
  add("created", r.created);
  add("recreated", r.recreated);
  add("started", r.started);
  add("removed", r.removed);
  if (!parts.length) return "Nothing changed";
  const s = parts.join("; ");
  return s[0].toUpperCase() + s.slice(1);
}

export function jobSummary(job: Job): string {
  if (job.state === "running") return "Deploying…";
  if (job.state === "failed") return job.error ?? "Failed";
  return resultSummary(job.result);
}

export type Tone = "good" | "warn" | "bad" | "accent" | "";

export const ACTION_LABEL: Record<string, string> = {
  deploy: "Deploy",
  "compose.save": "Compose edit",
  "env.save": "Environment edit",
  "git.commit": "Commit",
  "git.discard": "Discarded edits",
  "git.pull": "Pull",
  "git.push": "Push",
  "update.apply": "Update",
  "stack.adopt": "Stack adopted",
  "stack.create": "Stack created",
  "rollback.pin": "Rollback",
  "rollback.resume": "Resumed :latest",
};

/** A stack name as the server accepts it: lowercase letters, digits, - and _. */
export function stackName(s: string): string {
  return s
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "");
}

/** The compose project name compose derives from a folder. */
export function projectName(path: string): string {
  const base = path.replace(/\/+$/, "").split("/").pop() ?? "";
  return base
    .toLowerCase()
    .replace(/[^a-z0-9_-]/g, "")
    .replace(/^[-_]+/, "");
}

export function auditTone(e: AuditEvent): Tone {
  if (e.result === "failed") return "bad";
  if (e.result === "running") return "accent";
  return e.error ? "warn" : "good";
}

/** A short description of the branch state and how worried to be. */
export function gitLabel(git: GitStatus | undefined): { text: string; tone: Tone } {
  if (!git) return { text: "Not in git", tone: "" };
  if (git.untracked) return { text: "File not in git", tone: "warn" };
  if (git.modified) return { text: "Uncommitted changes", tone: "warn" };
  if (git.behind && git.ahead)
    return { text: `${git.ahead} ahead, ${git.behind} behind`, tone: "bad" };
  if (git.behind)
    return { text: `${git.behind} behind ${git.upstream ?? "remote"}`, tone: "accent" };
  if (git.ahead) return { text: `${git.ahead} not pushed`, tone: "warn" };
  return { text: `${git.branch} · ${git.head}`, tone: "good" };
}

export function stackTone(s: StackInfo): Tone {
  if (s.error || s.active?.state === "failed" || s.last?.state === "failed") return "bad";
  if (s.counts.running + (s.counts.asleep ?? 0) < s.counts.services) return "warn";
  return "good";
}

export function pendingText(n: number): string {
  if (n === 0) return "Up to date";
  return n === 1 ? "1 service to deploy" : `${n} services to deploy`;
}

export function pad(n: number): string {
  return String(n).padStart(2, "0");
}

const CONVENTIONAL =
  /^(feat|fix|chore|docs|refactor|perf|test|build|ci|style|revert)(\([a-z0-9._/-]+\))?!?: \S/;

/** Whether a commit message's first line is "<type>(<scope>): <summary>". */
export function isConventional(message: string): boolean {
  return CONVENTIONAL.test(message.split("\n")[0]);
}

/** Why a commit message would be refused, or "" when it's fine. */
export function messageProblem(message: string): string {
  if (!message.trim()) return "Write a commit message.";
  if (settings.conventional && !isConventional(message)) {
    return "Use Conventional Commits: <type>(<scope>): <summary>, e.g. chore(main-stack): bump shelfloom 0.4 → 0.5";
  }
  return "";
}

/** "6h0m0s" → "6h", "1h30m0s" → "1h30m". */
export function shortDuration(d: string): string {
  return d.replace(/0s$/, "").replace(/(\d+h)0m$/, "$1");
}

/** One line for a service's update, e.g. "0.4.1 → 0.5.0" or "new image". */
export function updateText(u: ServiceUpdate): string {
  if (u.latest) return `${tagOf(u.image)} → ${u.latest.tag}`;
  if (u.new_image) return "new image";
  return "";
}

export function tagOf(image: string): string {
  const slash = image.lastIndexOf("/");
  const colon = image.lastIndexOf(":");
  return colon > slash ? image.slice(colon + 1) : "latest";
}

export interface NewService {
  name: string;
  image: string;
  ports: { host: number; container: number; protocol: string }[];
  volumes: { host: string; container: string }[];
}

/**
 * Adds a service at the end of the file's `services:` mapping, matching the
 * file's indentation and whether it separates services with blank lines.
 */
export function insertService(content: string, svc: NewService): string {
  const lines = content.replace(/\n+$/, "").split("\n");
  let start = lines.findIndex((l) => /^services:\s*(\{\s*\})?\s*(#.*)?$/.test(l));
  if (start < 0) {
    if (lines.length === 1 && lines[0] === "") lines.pop();
    lines.push("services:");
    start = lines.length - 1;
  } else {
    lines[start] = "services:";
  }
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    if (/^[^\s#]/.test(lines[i])) {
      end = i;
      break;
    }
  }
  // Leave comments and blank lines above the next key where they are.
  while (end > start + 1 && /^\s*(#.*)?$/.test(lines[end - 1])) end--;

  const body = lines.slice(start + 1, end);
  const first = body.find((l) => /^\s+\S/.test(l) && !/^\s*#/.test(l));
  const u = first ? first.match(/^\s+/)![0] : "  ";
  const isServiceLine = (l: string | undefined) =>
    !!l && l.startsWith(u) && /^[^\s#]/.test(l.slice(u.length));
  const spaced = body.some((l, i) => i > 0 && l.trim() === "" && isServiceLine(body[i + 1]));

  const block = [
    `${u}${svc.name}:`,
    `${u}${u}image: ${svc.image}`,
    `${u}${u}restart: unless-stopped`,
  ];
  if (svc.ports.length) {
    block.push(`${u}${u}ports:`);
    for (const p of svc.ports) {
      block.push(`${u}${u}${u}- "${p.host}:${p.container}${p.protocol === "udp" ? "/udp" : ""}"`);
    }
  }
  if (svc.volumes.length) {
    block.push(`${u}${u}volumes:`);
    for (const v of svc.volumes) block.push(`${u}${u}${u}- ${v.host}:${v.container}`);
  }
  if (spaced && end > start + 1) block.unshift("");
  lines.splice(end, 0, ...block);
  return lines.join("\n") + "\n";
}

/** "sha256:0123456789ab…" → "0123456789ab". */
export function shortDigest(d: string | undefined): string {
  return d ? d.replace(/^sha256:/, "").slice(0, 12) : "";
}

/** How a deployed image reads: its commit when it has one, else its digest. */
export function imageVersion(i: { revision?: string; digest?: string; image_id?: string }): string {
  if (i.revision) return i.revision.slice(0, 7);
  return shortDigest(i.digest ?? i.image_id);
}

/** When a job started, from its ID ("20261002-150405-abcdef", UTC). */
export function jobTime(id: string): string {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})-/.exec(id);
  return m ? `${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}Z` : "";
}

/** A deploy's time as "2 Oct, 15:04" in local time. */
export function when(iso: string): string {
  if (!iso) return "";
  return new Date(iso).toLocaleString(undefined, {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** The release board's badge for an app. */
export function appStateLabel(a: { state: string; behind: number }): string {
  switch (a.state) {
    case "build-failed":
      return "Build failed";
    case "ready":
      return "Image ready, not deployed";
    case "building":
      return "Image building";
    case "not-built":
      return a.behind === 1 ? "1 commit, no image" : `${a.behind} commits, no image`;
    default:
      return "Deployed";
  }
}

export function prStateLabel(state: string): string {
  return (
    {
      ready: "Ready to merge",
      "ci-failing": "CI failing",
      "ci-running": "CI running",
      conflict: "Can't rebase",
      checking: "Checking",
      draft: "Draft",
      blocked: "Blocked",
      "changes-requested": "Changes requested",
    }[state] ?? state
  );
}

/** A workflow run in a word or two: "running", "passed", "failed"… */
export function runLabel(r: { name?: string; status: string; conclusion?: string }): string {
  const what =
    r.status !== "completed"
      ? r.status === "in_progress"
        ? "running"
        : "queued"
      : r.conclusion === "success"
        ? "passed"
        : (r.conclusion ?? "done").replace("_", " ");
  return r.name ? `${r.name}: ${what}` : what;
}
