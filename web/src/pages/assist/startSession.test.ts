import { describe, expect, it } from "vitest";
import { startSession } from "./startSession";

function deps(sendError?: Error) {
  const calls: string[] = [];
  return {
    calls,
    d: {
      create: async () => {
        calls.push("create");
        return { id: "s1" };
      },
      send: async (id: string) => {
        calls.push("send " + id);
        if (sendError) throw sendError;
      },
      end: async (id: string) => {
        calls.push("end " + id);
      },
      bind: (id: string) => {
        calls.push("bind " + id);
      },
    },
  };
}

describe("startSession", () => {
  it("binds the drawer only after the first message was accepted", async () => {
    const { calls, d } = deps();
    await startSession(d);
    expect(calls).toEqual(["create", "send s1", "bind s1"]);
  });

  it("ends the empty session, never binds, and rethrows when the send fails", async () => {
    const err = new Error("rejected");
    const { calls, d } = deps(err);
    await expect(startSession(d)).rejects.toBe(err);
    expect(calls).toEqual(["create", "send s1", "end s1"]);
  });

  it("still rethrows the send error when ending the session fails too", async () => {
    const err = new Error("rejected");
    const { d } = deps(err);
    d.end = async () => {
      throw new Error("gone");
    };
    await expect(startSession(d)).rejects.toBe(err);
  });
});
