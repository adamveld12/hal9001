import { describe, expect, it } from "vitest";
import {
  agentRunFiltersFromParams,
  agentRunFiltersToQuery,
  agentRunOwner,
  filtersToParams,
  flowRunFiltersFromParams,
  flowRunFiltersToQuery,
  hasActive,
  NO_FLOW_RUN_FILTERS,
} from "./runList";

describe("flow run list filters", () => {
  it("defaults to an empty query so the server hides test runs", () => {
    const f = flowRunFiltersFromParams(new URLSearchParams());
    expect(f).toEqual(NO_FLOW_RUN_FILTERS);
    expect(flowRunFiltersToQuery(f)).toEqual({});
    expect(hasActive(f)).toBe(false);
  });

  it("round-trips through URL params and drops unknown enum values", () => {
    const f = { flow: "triage", status: "failed", kind: "test" } as const;
    expect(flowRunFiltersFromParams(filtersToParams(f))).toEqual(f);
    expect(
      flowRunFiltersFromParams(new URLSearchParams("status=x&kind=prod")),
    ).toEqual(NO_FLOW_RUN_FILTERS);
  });

  it("keeps unrelated params (view) and removes cleared filters", () => {
    const base = new URLSearchParams("view=runs&status=failed");
    const next = filtersToParams(
      { ...NO_FLOW_RUN_FILTERS, kind: "replay" },
      base,
    );
    expect(next.toString()).toBe("view=runs&kind=replay");
  });

  it("pins the flow id over the flow filter", () => {
    const q = flowRunFiltersToQuery(
      { flow: "other", status: "running", kind: "replay" },
      "mine",
    );
    expect(q).toEqual({ flow_id: "mine", status: "running", kind: "replay" });
  });
});

describe("agent run list filters", () => {
  it("parses and maps status and runtime", () => {
    const f = agentRunFiltersFromParams(
      new URLSearchParams("status=timeout&runtime=claude"),
    );
    expect(agentRunFiltersToQuery(f)).toEqual({
      status: "timeout",
      runtime: "claude",
    });
    expect(
      agentRunFiltersFromParams(new URLSearchParams("status=done")).status,
    ).toBe("");
  });

  it("classifies the owner of a run", () => {
    expect(
      agentRunOwner({
        flow_id: "f",
        flow_run_id: "r",
        assist_session_id: null,
      }),
    ).toEqual({ kind: "flow", flowId: "f", flowRunId: "r" });
    expect(
      agentRunOwner({
        flow_id: null,
        flow_run_id: null,
        assist_session_id: "s",
      }),
    ).toEqual({ kind: "assist", sessionId: "s" });
    expect(
      agentRunOwner({
        flow_id: null,
        flow_run_id: null,
        assist_session_id: null,
      }),
    ).toEqual({ kind: "none" });
  });
});
