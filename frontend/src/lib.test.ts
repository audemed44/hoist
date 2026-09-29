import { describe, expect, it } from "vitest";
import { ago, gitLabel, isConventional, resultSummary } from "./lib";
import { href, parseRoute } from "./router";
import type { GitStatus } from "./types";

describe("ago", () => {
  const now = Date.parse("2026-09-30T12:00:00Z");
  it("rounds down", () => {
    expect(ago("2026-09-30T11:59:30Z", now)).toBe("just now");
    expect(ago("2026-09-30T11:15:00Z", now)).toBe("45m ago");
    expect(ago("2026-09-29T12:00:00Z", now)).toBe("24h ago");
    expect(ago("2026-09-20T12:00:00Z", now)).toBe("10d ago");
  });
});

describe("resultSummary", () => {
  it("lists what changed", () => {
    expect(
      resultSummary({
        created: ["new"],
        recreated: ["a", "b"],
        removed: [],
        started: [],
        updated: ["a"],
      }),
    ).toBe("Created new; recreated a, b");
    expect(
      resultSummary({ created: [], recreated: [], removed: [], started: [], updated: [] }),
    ).toBe("Nothing changed");
  });
});

describe("gitLabel", () => {
  const base: GitStatus = {
    branch: "main",
    head: "abc1234",
    upstream: "origin/main",
    ahead: 0,
    behind: 0,
    modified: false,
    untracked: false,
  };
  it("says what needs attention", () => {
    expect(gitLabel(undefined).text).toBe("Not in git");
    expect(gitLabel(base)).toEqual({ text: "main · abc1234", tone: "good" });
    expect(gitLabel({ ...base, behind: 2 }).text).toBe("2 behind origin/main");
    expect(gitLabel({ ...base, ahead: 1 }).tone).toBe("warn");
    expect(gitLabel({ ...base, modified: true, behind: 1 }).text).toBe("Uncommitted changes");
  });
});

describe("routes", () => {
  it("round-trips", () => {
    for (const path of [
      "/",
      "/stacks/main-stack",
      "/stacks/main-stack/env",
      "/jobs/20260930-120000-abcdef",
    ]) {
      expect(href(parseRoute(path))).toBe(path);
    }
  });
  it("falls back", () => {
    expect(parseRoute("/stacks/kopia/nope")).toEqual({
      page: "stack",
      name: "kopia",
      tab: "services",
    });
    expect(parseRoute("/whatever")).toEqual({ page: "home" });
  });
});

describe("isConventional", () => {
  it("matches the server's rule", () => {
    expect(isConventional("chore(main-stack): bump shelfloom 0.4 → 0.5")).toBe(true);
    expect(isConventional("feat!: drop kopia")).toBe(true);
    expect(isConventional("main-stack: bump shelfloom")).toBe(false);
    expect(isConventional("feat(Main): x")).toBe(false);
  });
});
