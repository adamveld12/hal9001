import { ApiError, type Problem } from "../../api/client";
import { diagnosticMessages, isConflict } from "./problem";
import { describe, expect, it } from "vitest";
import {
  buildPatch,
  formFromConfig,
  isStale,
  validateForm,
  type SettingsForm,
} from "./settingsForm";
import { envKeyError } from "./envKey";

const config = {
  max_agent_runs: 3,
  max_command_runs: 8,
  retention_days: 30,
  dedup_retention_days: 180,
  notify_min_priority: "high" as const,
  approval_timeout: "1h",
  max_steers_per_run: 3,
};

describe("formFromConfig", () => {
  it("shows the daemon defaults for keys config.yaml omits", () => {
    expect(formFromConfig({})).toEqual({
      max_agent_runs: "2",
      max_command_runs: "8",
      retention_days: "30",
      dedup_retention_days: "180",
      notify_min_priority: "normal",
      approval_timeout: "1h",
      max_steers_per_run: "3",
    });
  });

  it("uses configured values", () => {
    const f = formFromConfig(config);
    expect(f.max_agent_runs).toBe("3");
    expect(f.notify_min_priority).toBe("high");
  });
});

describe("buildPatch", () => {
  const base = formFromConfig(config);

  it("is undefined when nothing changed, even if numbers are re-typed with spaces", () => {
    expect(buildPatch(base, base, "h1")).toBeUndefined();
    expect(
      buildPatch(base, { ...base, max_agent_runs: " 3 " }, "h1"),
    ).toBeUndefined();
  });

  it("sends only changed fields, numbers as numbers, with the hash it was based on", () => {
    const form: SettingsForm = {
      ...base,
      max_agent_runs: "4",
      notify_min_priority: "urgent",
      approval_timeout: " 30m ",
    };
    expect(buildPatch(base, form, "abc")).toEqual({
      expected_hash: "abc",
      max_agent_runs: 4,
      notify_min_priority: "urgent",
      approval_timeout: "30m",
    });
  });
});

describe("validateForm", () => {
  const ok = formFromConfig(config);

  it("accepts a valid form", () => {
    expect(validateForm(ok)).toEqual({});
  });

  it("rejects zero, negatives, fractions, blanks, and text for counts", () => {
    for (const bad of ["0", "-1", "1.5", "", "two", "1e3"]) {
      expect(validateForm({ ...ok, retention_days: bad })).toHaveProperty(
        "retention_days",
      );
    }
  });

  it("rejects durations the API pattern would reject", () => {
    for (const bad of ["", "1", "h", "1 h", "-5m", "1w"]) {
      expect(validateForm({ ...ok, approval_timeout: bad })).toHaveProperty(
        "approval_timeout",
      );
    }
    for (const good of ["90s", "1.5h", "1h30m", "7d"]) {
      expect(validateForm({ ...ok, approval_timeout: good })).toEqual({});
    }
  });
});

describe("max_steers_per_run", () => {
  const ok = formFromConfig(config);

  it("is a whole number from 1 to 100", () => {
    for (const bad of ["0", "101", "-1", "", "x", "2.5"]) {
      expect(validateForm({ ...ok, max_steers_per_run: bad })).toHaveProperty(
        "max_steers_per_run",
      );
    }
    for (const good of ["1", "3", "100"]) {
      expect(validateForm({ ...ok, max_steers_per_run: good })).toEqual({});
    }
  });

  it("is sent as a number when changed", () => {
    expect(buildPatch(ok, { ...ok, max_steers_per_run: "5" }, "h")).toEqual({
      expected_hash: "h",
      max_steers_per_run: 5,
    });
  });
});

describe("isStale", () => {
  it("flags a draft started from an older config.yaml, not an untouched form", () => {
    const form = formFromConfig(config);
    expect(isStale(null, "h2")).toBe(false);
    expect(isStale({ form, baseHash: "h1" }, "h1")).toBe(false);
    expect(isStale({ form, baseHash: "h1" }, "h2")).toBe(true);
  });
});

describe("envKeyError", () => {
  it("accepts shell-style names", () => {
    expect(envKeyError("GITHUB_TOKEN")).toBeUndefined();
    expect(envKeyError("_x1")).toBeUndefined();
  });

  it("rejects empty, digit-first, punctuation, and over-long names", () => {
    expect(envKeyError("")).toBeDefined();
    expect(envKeyError("1A")).toBeDefined();
    expect(envKeyError("A-B")).toBeDefined();
    expect(envKeyError("A B")).toBeDefined();
    expect(envKeyError("A".repeat(129))).toBeDefined();
    expect(envKeyError("A".repeat(128))).toBeUndefined();
  });
});

function apiError(status: number, kind: string, extra = {}): ApiError {
  return new ApiError(
    status,
    {
      type: `urn:hal9001:problem:${kind}`,
      title: "t",
      status,
      ...extra,
    } as Problem,
    "fallback",
  );
}

describe("problem helpers", () => {
  it("recognises a hash conflict and nothing else", () => {
    expect(isConflict(apiError(409, "conflict"))).toBe(true);
    expect(isConflict(apiError(409, "invalid-state"))).toBe(false);
    expect(isConflict(new Error("conflict"))).toBe(false);
  });

  it("lists diagnostics with their paths", () => {
    const e = apiError(422, "validation-failed", {
      diagnostics: [
        {
          severity: "error",
          code: "invalid_duration",
          message: "bad",
          path: "approval_timeout",
        },
        { severity: "error", code: "required", message: "missing" },
        { nope: true },
      ],
    });
    expect(diagnosticMessages(e)).toEqual(["approval_timeout: bad", "missing"]);
    expect(diagnosticMessages(apiError(400, "bad-request"))).toEqual([]);
    expect(diagnosticMessages("x")).toEqual([]);
  });
});
