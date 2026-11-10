import { describe, expect, it } from "vitest";
import {
  denialKind,
  exitBadge,
  lastEntryId,
  mergeSandboxLog,
  outputOpen,
  provisioningMs,
  type SandboxLogEntry,
  type SandboxSession,
} from "./sandboxEntries";

const entry = (id: number, extra: Partial<SandboxLogEntry> = {}) =>
  ({
    id,
    session_id: "S1",
    ts: "2026-01-01T00:00:00Z",
    kind: "exec",
    level: "info",
    title: `e${id}`,
    ...extra,
  }) as SandboxLogEntry;

const session = (extra: Partial<SandboxSession> = {}) =>
  ({
    id: "S1",
    status: "provisioning",
    started_at: "2026-01-01T00:00:00Z",
    entry_count: 0,
    ...extra,
  }) as SandboxSession;

describe("mergeSandboxLog", () => {
  it("appends new entries, replaces re-sent ones and takes the newest sessions", () => {
    const prev = {
      sessions: [session()],
      entries: [entry(1), entry(2)],
      hasMore: true,
    };
    const next = mergeSandboxLog(prev, {
      sessions: [session({ status: "ready" })],
      entries: [entry(2, { stdout: "out" }), entry(3)],
    });
    expect(next.entries.map((e) => e.id)).toEqual([1, 2, 3]);
    expect(next.entries[1].stdout).toBe("out");
    expect(next.sessions[0].status).toBe("ready");
    expect(next.hasMore).toBe(true);
    expect(lastEntryId(next.entries)).toBe(3);
  });

  it("starts from the first response", () => {
    const next = mergeSandboxLog(undefined, {
      sessions: [session()],
      entries: [entry(5)],
    });
    expect(next.entries).toHaveLength(1);
  });
});

describe("log entry labels", () => {
  it("tells denial reasons apart", () => {
    expect(denialKind({ approval: "denied by policy: no network" })).toBe(
      "policy",
    );
    expect(denialKind({ approval: "denied by user" })).toBe("user");
    expect(denialKind({ approval: "expired" })).toBe("expired");
  });

  it("colours exit codes and shows timeouts first", () => {
    expect(exitBadge({ exit_code: 0 })?.tone).toBe("ok");
    expect(exitBadge({ exit_code: 2 })?.tone).toBe("bad");
    expect(exitBadge({ exit_code: 124, timed_out: true })?.tone).toBe("warn");
    expect(exitBadge({})).toBeNull();
  });

  it("expands output of failed commands and a lone stream only", () => {
    expect(
      outputOpen({ exit_code: 1, stdout: "a", stderr: "b" }, "stdout"),
    ).toBe(true);
    expect(
      outputOpen({ exit_code: 0, stdout: "a", stderr: "b" }, "stdout"),
    ).toBe(false);
    expect(outputOpen({ exit_code: 0, stdout: "a" }, "stdout")).toBe(true);
  });

  it("measures provisioning only when the sandbox became ready", () => {
    expect(provisioningMs(session({ ready_at: "2026-01-01T00:00:12Z" }))).toBe(
      12000,
    );
    expect(provisioningMs(session({ ready_at: null }))).toBeNull();
  });
});
