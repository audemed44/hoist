import type { Change, GitStatus, Job, JobResult, StackInfo } from "./types";

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
  if (s.counts.running < s.counts.services) return "warn";
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
