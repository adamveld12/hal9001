import { describe, expect, it } from "vitest";
import {
  CLOSED_DRAWER,
  drawerReducer,
  GENERAL_TARGET,
  parseDrawer,
  serializeDrawer,
  type DrawerState,
} from "./drawerState";

const sensorGh = { kind: "sensor", id: "gh" } as const;
const withSession: DrawerState = {
  open: true,
  sessionId: "s1",
  target: sensorGh,
  seed: null,
};

describe("drawerReducer", () => {
  it("opens and closes without touching the session", () => {
    const closed = drawerReducer(withSession, { type: "close" });
    expect(closed).toEqual({ ...withSession, open: false });
    expect(drawerReducer(closed, { type: "open" })).toEqual(withSession);
  });

  it("binds a new session and opens the drawer on it", () => {
    const s = drawerReducer(
      { ...CLOSED_DRAWER, seed: { text: "x", n: 1 } },
      { type: "bind", sessionId: "s2", target: sensorGh },
    );
    expect(s).toEqual({
      open: true,
      sessionId: "s2",
      target: sensorGh,
      seed: null,
    });
  });

  it("keeps the shown session when its own target is opened, otherwise starts that target", () => {
    const same = drawerReducer(withSession, {
      type: "openTarget",
      target: sensorGh,
    });
    expect(same.sessionId).toBe("s1");
    const other = drawerReducer(withSession, {
      type: "openTarget",
      target: { kind: "flow", id: null },
      seed: { text: "hi", n: 2 },
    });
    expect(other).toEqual({
      open: true,
      sessionId: null,
      target: { kind: "flow", id: null },
      seed: { text: "hi", n: 2 },
    });
  });

  it("starts a general chat without closing the drawer", () => {
    expect(drawerReducer(withSession, { type: "newChat" })).toEqual({
      open: true,
      sessionId: null,
      target: GENERAL_TARGET,
      seed: null,
    });
  });

  it("selects a session with its target and clears the seed", () => {
    const s = drawerReducer(
      { ...CLOSED_DRAWER, seed: { text: "x", n: 1 } },
      { type: "select", sessionId: "s9", target: { kind: "tool", id: "t" } },
    );
    expect(s).toEqual({
      open: true,
      sessionId: "s9",
      target: { kind: "tool", id: "t" },
      seed: null,
    });
  });

  it("sets the composer seed and clears it once taken", () => {
    const seeded = drawerReducer(withSession, {
      type: "seed",
      seed: { text: "hello", n: 3 },
    });
    expect(seeded.seed).toEqual({ text: "hello", n: 3 });
    expect(drawerReducer(seeded, { type: "seed", seed: null }).seed).toBeNull();
  });

  it("retargets without changing session or open state", () => {
    const s = drawerReducer(
      { ...withSession, target: { kind: "sensor", id: null } },
      { type: "retarget", target: sensorGh },
    );
    expect(s).toEqual(withSession);
  });
});

describe("drawer persistence", () => {
  it("round-trips open, session and target", () => {
    expect(parseDrawer(serializeDrawer(withSession))).toEqual(withSession);
  });

  it("falls back to a closed drawer for missing or malformed values", () => {
    for (const raw of [
      null,
      "",
      "not json",
      "42",
      "null",
      '{"open":"yes","sessionId":null,"target":{"kind":"general","id":null}}',
      '{"open":true,"sessionId":7,"target":{"kind":"general","id":null}}',
      '{"open":true,"sessionId":null,"target":{"kind":"robot","id":null}}',
      '{"open":true,"sessionId":null,"target":{"kind":"sensor","id":5}}',
      '{"open":true,"sessionId":null,"target":{"kind":"general","id":"x"}}',
      '{"open":true,"sessionId":null}',
    ]) {
      expect(parseDrawer(raw)).toEqual(CLOSED_DRAWER);
    }
  });
});
