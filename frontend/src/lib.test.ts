import { describe, expect, it } from "vitest";
import {
  ago,
  gitLabel,
  insertService,
  isConventional,
  projectName,
  resultSummary,
  shortDuration,
  stackName,
  updateText,
  imageVersion,
  jobTime,
  shortDigest,
} from "./lib";
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
      "/audit",
      "/releases",
      "/new",
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

describe("updates", () => {
  it("describes them", () => {
    expect(shortDuration("6h0m0s")).toBe("6h");
    expect(shortDuration("1h30m0s")).toBe("1h30m");
    expect(
      updateText({
        service: "a",
        image: "ghcr.io/x/a:0.4.1",
        policy: "off",
        latest: { tag: "0.5.0", bump: "minor" },
      }),
    ).toBe("0.4.1 → 0.5.0");
    expect(
      updateText({ service: "b", image: "localhost:5000/b", policy: "off", new_image: true }),
    ).toBe("new image");
  });
});

describe("names", () => {
  it("cleans stack names", () => {
    expect(stackName(" My Stack! ")).toBe("my-stack");
    expect(stackName("romm")).toBe("romm");
    expect(stackName("--x_")).toBe("x");
  });
  it("derives project names like compose", () => {
    expect(projectName("/home/u/homelab/Main Stack/")).toBe("mainstack");
    expect(projectName("/srv/_romm")).toBe("romm");
  });
});

describe("insertService", () => {
  const svc = {
    name: "romm",
    image: "ghcr.io/rommapp/romm:5.3.1",
    ports: [
      { host: 8081, container: 8080, protocol: "tcp" },
      { host: 5353, container: 53, protocol: "udp" },
    ],
    volumes: [{ host: "./romm/data", container: "/data" }],
  };
  it("appends to the services mapping, before the next key", () => {
    const file =
      "services:\n  web:\n    image: nginx:1\n\n  db:\n    image: postgres:17\n\n# shared\nvolumes:\n  x: {}\n";
    expect(insertService(file, svc)).toBe(
      "services:\n  web:\n    image: nginx:1\n\n  db:\n    image: postgres:17\n\n" +
        "  romm:\n    image: ghcr.io/rommapp/romm:5.3.1\n    restart: unless-stopped\n" +
        '    ports:\n      - "8081:8080"\n      - "5353:53/udp"\n' +
        "    volumes:\n      - ./romm/data:/data\n" +
        "\n# shared\nvolumes:\n  x: {}\n",
    );
  });
  it("follows the file's indentation, without blank lines", () => {
    const file = "services:\n    web:\n        image: nginx:1\n";
    expect(insertService(file, { ...svc, ports: [], volumes: [] })).toBe(
      "services:\n    web:\n        image: nginx:1\n" +
        "    romm:\n        image: ghcr.io/rommapp/romm:5.3.1\n        restart: unless-stopped\n",
    );
  });
  it("starts a services mapping when there is none", () => {
    expect(insertService("services: {}\n", { ...svc, ports: [], volumes: [] })).toBe(
      "services:\n  romm:\n    image: ghcr.io/rommapp/romm:5.3.1\n    restart: unless-stopped\n",
    );
    expect(insertService("", { ...svc, ports: [], volumes: [] })).toBe(
      "services:\n  romm:\n    image: ghcr.io/rommapp/romm:5.3.1\n    restart: unless-stopped\n",
    );
  });
});

describe("deploy versions", () => {
  it("reads a job's start time from its ID", () => {
    expect(jobTime("20261002-150405-abcdef")).toBe("2026-10-02T15:04:05Z");
    expect(jobTime("nope")).toBe("");
  });
  it("shows a commit when there is one, else a short digest", () => {
    expect(imageVersion({ revision: "72c8c1cbe506c2a6", digest: "sha256:aaa" })).toBe("72c8c1c");
    expect(imageVersion({ digest: "sha256:0123456789abcdef" })).toBe("0123456789ab");
    expect(shortDigest(undefined)).toBe("");
  });
});
