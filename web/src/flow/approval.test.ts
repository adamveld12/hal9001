import { describe, expect, it } from "vitest";
import {
  DRY_RUN_NOTICE,
  fieldChoices,
  lookupPath,
  previewApproval,
  renderTemplate,
  toggleField,
} from "./approval";
import { newNode, type ApprovalNode } from "./model";
import { validateFlow } from "./validate";

const sample = {
  title: "Fix it",
  author: { login: "ada" },
  labels: ["a", "b"],
  lines: 12,
  gone: null,
};

function node(over: Partial<ApprovalNode> = {}): ApprovalNode {
  return {
    id: "ask",
    type: "approval",
    title: "Ship {{.input.title}}?",
    description: "Asks whether to ship",
    ...over,
  };
}

describe("renderTemplate", () => {
  it("fills input and event variables, tolerating spaces", () => {
    expect(
      renderTemplate("{{.input.title}} by {{ .event.author.login }}", {
        input: sample,
        event: sample,
      }),
    ).toBe("Fix it by ada");
  });

  it("renders missing values empty and objects as JSON", () => {
    expect(
      renderTemplate("[{{.input.nope}}] {{.input.author}}", {
        input: sample,
        event: sample,
      }),
    ).toBe('[] {"login":"ada"}');
  });

  it("keeps expressions it cannot evaluate", () => {
    expect(
      renderTemplate("{{if .input.x}}y{{end}}", {
        input: sample,
        event: sample,
      }),
    ).toBe("{{if .input.x}}y{{end}}");
  });
});

describe("previewApproval", () => {
  it("renders title, body and the fields table from the sample", () => {
    const p = previewApproval(
      node({
        body: "**{{.input.author.login}}**",
        fields: ["lines", "author.login", "gone", "labels.1"],
        priority: "high",
      }),
      sample,
    );
    expect(p).toMatchObject({
      title: "Ship Fix it?",
      body_md: "**ada**",
      priority: "high",
      timeout: null,
      on_timeout: null,
    });
    expect(p.fields).toEqual([
      { key: "lines", value: "12" },
      { key: "author.login", value: "ada" },
      { key: "gone", value: "" },
      { key: "labels.1", value: "b" },
    ]);
  });

  it("defaults priority and on_timeout, and only reports on_timeout with a timeout", () => {
    expect(previewApproval(node(), sample).priority).toBe("normal");
    expect(previewApproval(node({ timeout: "1h" }), sample)).toMatchObject({
      timeout: "1h",
      on_timeout: "denied",
    });
    expect(
      previewApproval(node({ timeout: "1h", on_timeout: "error" }), sample)
        .on_timeout,
    ).toBe("error");
  });

  it("starts a dry run's body with the dry-run notice", () => {
    const p = previewApproval(node({ body: "details" }), sample, {
      dryRun: true,
    });
    expect(p.body_md).toBe(`${DRY_RUN_NOTICE}\n\ndetails`);
    expect(previewApproval(node(), sample, { dryRun: true }).body_md).toBe(
      DRY_RUN_NOTICE,
    );
  });
});

describe("fields picker", () => {
  it("offers scalar and nested paths of the sample, without whole-item or per-element paths", () => {
    const paths = fieldChoices(sample);
    expect(paths).toContain("author.login");
    expect(paths).toContain("lines");
    expect(paths).not.toContain(".");
    expect(paths.some((p) => p.includes("[]"))).toBe(false);
    expect(fieldChoices(undefined)).toEqual([]);
  });

  it("toggles a path in and out of the list", () => {
    expect(toggleField(["a"], "b")).toEqual(["a", "b"]);
    expect(toggleField(["a", "b"], "a")).toEqual(["b"]);
  });

  it("looks paths up through arrays and objects", () => {
    expect(lookupPath(sample, "labels.0")).toBe("a");
    expect(lookupPath(sample, "labels.9")).toBeUndefined();
    expect(lookupPath(sample, "author.login.x")).toBeUndefined();
  });
});

describe("approval node validation", () => {
  const flow = (n: ApprovalNode) => ({
    id: "f",
    enabled: true,
    dry_run: false,
    subscribe: [],
    nodes: [n],
    edges: [],
  });

  it("needs a description", () => {
    const fresh = newNode("approval", "ask", { x: 0, y: 0 }) as ApprovalNode;
    expect(fresh.description).toBe("");
    const errs = (n: ApprovalNode) =>
      validateFlow(flow(n)).filter(
        (d) => d.severity === "error" && d.path?.endsWith(".description"),
      );
    expect(errs(fresh)).toHaveLength(1);
    expect(errs({ ...fresh, description: "  " })).toHaveLength(1);
    expect(errs({ ...fresh, description: "Deploy {{.input.name}}?" })).toEqual(
      [],
    );
  });

  it("needs a title, and on_timeout needs a timeout", () => {
    const base = {
      ...(newNode("approval", "ask", { x: 0, y: 0 }) as ApprovalNode),
      description: "Asks whether to continue",
    };
    expect(
      validateFlow(flow(base)).filter((d) => d.severity === "error"),
    ).toEqual([]);
    expect(
      validateFlow(flow({ ...base, title: " " })).map((d) => d.message),
    ).toContain("an approval needs a title");
    expect(
      validateFlow(flow({ ...base, on_timeout: "approved" })).map(
        (d) => d.message,
      ),
    ).toContain("on_timeout only applies with a timeout");
    expect(
      validateFlow(
        flow({ ...base, on_timeout: "approved", timeout: "1h" }),
      ).filter((d) => d.severity === "error"),
    ).toEqual([]);
  });
});

describe("previewApproval editable fields", () => {
  it("lists editable paths with their sample values (null when missing)", () => {
    const p = previewApproval(
      node({ editable: ["title", "labels", "nope"] }),
      sample,
    );
    expect(p.editable).toEqual([
      { key: "title", value: "Fix it" },
      { key: "labels", value: ["a", "b"] },
      { key: "nope", value: null },
    ]);
  });
});
