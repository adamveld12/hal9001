import { describe, expect, it } from "vitest";
import {
  buildSampleInput,
  upstreamNodeIds,
  type SampleState,
} from "./testNode";

const base: SampleState = {
  source: "json",
  eventId: "",
  nodeRunId: "",
  outputIndex: "",
  json: "",
};

describe("buildSampleInput", () => {
  it("requires a selection for event and node-run sources", () => {
    expect(buildSampleInput({ ...base, source: "event" })).toMatchObject({
      ok: false,
    });
    expect(
      buildSampleInput({ ...base, source: "event", eventId: "01E" }),
    ).toEqual({
      ok: true,
      input: { source: "event", event_id: "01E" },
    });
    expect(buildSampleInput({ ...base, source: "node_run" })).toMatchObject({
      ok: false,
    });
  });

  it("defaults the output index to 0 and rejects negative or fractional ones", () => {
    const pick = { ...base, source: "node_run" as const, nodeRunId: "01N" };
    expect(buildSampleInput(pick)).toEqual({
      ok: true,
      input: { source: "node_run", node_run_id: "01N", output_index: 0 },
    });
    expect(buildSampleInput({ ...pick, outputIndex: "2" })).toMatchObject({
      ok: true,
    });
    expect(buildSampleInput({ ...pick, outputIndex: "-1" })).toMatchObject({
      ok: false,
    });
    expect(buildSampleInput({ ...pick, outputIndex: "1.5" })).toMatchObject({
      ok: false,
    });
  });

  it("parses pasted JSON and reports syntax errors", () => {
    expect(
      buildSampleInput({ ...base, json: '{"reason":"review_requested"}' }),
    ).toEqual({
      ok: true,
      input: { source: "json", json: { reason: "review_requested" } },
    });
    expect(buildSampleInput({ ...base, json: "{nope" })).toMatchObject({
      ok: false,
    });
    expect(buildSampleInput({ ...base, json: "null" })).toEqual({
      ok: true,
      input: { source: "json", json: null },
    });
  });
});

describe("upstreamNodeIds", () => {
  it("lists distinct nodes feeding the target", () => {
    const edges = [
      { from: "a", to: "c" },
      { from: "a", to: "c" },
      { from: "b", to: "c" },
      { from: "c", to: "d" },
    ];
    expect(upstreamNodeIds(edges, "c")).toEqual(["a", "b"]);
    expect(upstreamNodeIds(edges, "a")).toEqual([]);
  });
});
