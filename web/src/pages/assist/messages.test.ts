import { describe, expect, it } from "vitest";
import type {
  AssistMessage,
  AssistSession,
  AssistSessionDetail,
  AssistStreamMessage,
} from "../../api/assist";
import {
  applyMessageEvent,
  applySessionEvent,
  mergeMessage,
  traceLinks,
} from "./messages";

function msg(over: Partial<AssistMessage> & { id: string }): AssistMessage {
  return {
    role: "assistant",
    text: "",
    agent_run_id: "run1",
    tool_call: null,
    approval_id: null,
    created_at: "2026-11-05T10:00:00Z",
    ...over,
  };
}

describe("mergeMessage", () => {
  it("replaces a message in place, keeping its position", () => {
    const list = [
      msg({ id: "1", text: "a" }),
      msg({ id: "2", text: "b" }),
      msg({ id: "3", text: "c" }),
    ];
    const out = mergeMessage(list, msg({ id: "2", text: "b, more" }));
    expect(out.map((m) => m.text)).toEqual(["a", "b, more", "c"]);
  });

  it("appends a newer message and orders an out-of-order one by creation time", () => {
    const list = [
      msg({ id: "1", created_at: "2026-11-05T10:00:01Z" }),
      msg({ id: "3", created_at: "2026-11-05T10:00:03Z" }),
    ];
    const out = mergeMessage(
      list,
      msg({ id: "2", created_at: "2026-11-05T10:00:02Z" }),
    );
    expect(out.map((m) => m.id)).toEqual(["1", "2", "3"]);
    expect(
      mergeMessage(
        out,
        msg({ id: "4", created_at: "2026-11-05T10:00:09Z" }),
      ).at(-1)?.id,
    ).toBe("4");
  });

  it("orders messages created at the same time by id", () => {
    const out = mergeMessage([msg({ id: "b" })], msg({ id: "a" }));
    expect(out.map((m) => m.id)).toEqual(["a", "b"]);
  });

  it("is idempotent when the same event is delivered twice", () => {
    const m = msg({ id: "1", text: "x" });
    const once = mergeMessage([], m);
    expect(mergeMessage(once, m)).toEqual(once);
  });

  it("does not modify the list it was given", () => {
    const list = [msg({ id: "1" })];
    mergeMessage(list, msg({ id: "2", created_at: "2026-11-05T11:00:00Z" }));
    expect(list).toHaveLength(1);
  });
});

const session: AssistSession = {
  id: "s1",
  target: { kind: "sensor", id: null },
  runtime: "claude",
  model: null,
  status: "idle",
  error: null,
  draft: {
    kind: "sensor",
    yaml: "",
    hash: "h",
    sensor_id: null,
    flow_id: null,
    prompts: [],
    diagnostics: [],
    updated_at: "2026-11-05T10:00:00Z",
    updated_by: "user",
  },
  last_agent_run_id: null,
  created_at: "2026-11-05T10:00:00Z",
  updated_at: "2026-11-05T10:00:00Z",
};
const detail: AssistSessionDetail = {
  ...session,
  messages: [msg({ id: "1" })],
};

describe("applying stream events to a cached session", () => {
  it("adds a streamed message and drops its session_id", () => {
    const ev: AssistStreamMessage = {
      ...msg({ id: "2", created_at: "2026-11-05T10:00:05Z" }),
      session_id: "s1",
    };
    const out = applyMessageEvent(detail, ev);
    expect(out.messages.map((m) => m.id)).toEqual(["1", "2"]);
    expect("session_id" in out.messages[1]!).toBe(false);
  });

  it("ignores messages of another session", () => {
    const ev: AssistStreamMessage = {
      ...msg({ id: "2" }),
      session_id: "other",
    };
    expect(applyMessageEvent(detail, ev)).toBe(detail);
  });

  it("takes a newer session snapshot and keeps the messages", () => {
    const out = applySessionEvent(detail, {
      ...session,
      status: "running",
      updated_at: "2026-11-05T10:00:09Z",
    });
    expect(out.status).toBe("running");
    expect(out.messages).toBe(detail.messages);
  });

  it("ignores a snapshot older than the cached one", () => {
    const newer = {
      ...detail,
      status: "running" as const,
      updated_at: "2026-11-05T10:00:09Z",
    };
    expect(applySessionEvent(newer, session)).toBe(newer);
  });
});

describe("traceLinks", () => {
  it("links the last message of each turn to its agent run", () => {
    const messages = [
      msg({ id: "u1", role: "user", agent_run_id: null }),
      msg({ id: "a1", agent_run_id: "run1" }),
      msg({ id: "t1", role: "tool", agent_run_id: "run1" }),
      msg({ id: "a2", agent_run_id: "run1" }),
      msg({ id: "u2", role: "user", agent_run_id: null }),
      msg({ id: "a3", agent_run_id: "run2" }),
    ];
    expect([...traceLinks(messages)]).toEqual([
      ["a2", "run1"],
      ["a3", "run2"],
    ]);
  });

  it("does not link a turn that has no agent-side message yet", () => {
    expect(
      traceLinks([msg({ id: "u1", role: "user", agent_run_id: null })]).size,
    ).toBe(0);
  });

  it("splits turns when the run id changes without a user message between", () => {
    const out = traceLinks([
      msg({ id: "a1", agent_run_id: "run1" }),
      msg({ id: "a2", agent_run_id: "run2" }),
    ]);
    expect([...out]).toEqual([
      ["a1", "run1"],
      ["a2", "run2"],
    ]);
  });
});
