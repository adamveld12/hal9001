// Logic behind the "Request approval" node's inspector: rendering the approval as it will look, against a sample.

import type { ApprovalNode, Priority } from "./model";
import { discoverFields } from "./transform/fields";

/** First line of the body of a dry run's approval (the daemon writes the same text). */
export const DRY_RUN_NOTICE =
  "This is a dry run: approving or denying only exercises the flow's branches.";

export interface ApprovalPreview {
  title: string;
  body_md: string;
  fields: { key: string; value: string }[];
  priority: Priority;
  /** The node's timeout as written; null without one. */
  timeout: string | null;
  on_timeout: string | null;
}

/** Follow a dot-path (`a.b.0.c`) through JSON; a missing step gives undefined. */
export function lookupPath(value: unknown, path: string): unknown {
  let v: unknown = value;
  for (const seg of path.split(".")) {
    if (Array.isArray(v)) {
      const i = Number(seg);
      if (!Number.isInteger(i) || i < 0 || i >= v.length) return undefined;
      v = v[i];
    } else if (typeof v === "object" && v !== null) {
      v = (v as Record<string, unknown>)[seg];
    } else {
      return undefined;
    }
  }
  return v;
}

/** Table text of a field value: strings as they are, null/missing as empty, the rest as compact JSON. */
export function fieldText(v: unknown): string {
  if (v === undefined || v === null) return "";
  return typeof v === "string" ? v : JSON.stringify(v);
}

/**
 * Render a Go-style template against a sample for the preview. Only plain
 * variable references are evaluated (`{{.input.a.b}}`, `{{ .event.x }}`);
 * anything else (functions, conditionals) is kept as written, so the preview
 * never claims a result it cannot know.
 */
export function renderTemplate(
  tmpl: string,
  data: { input: unknown; event: unknown },
): string {
  return tmpl.replace(
    /\{\{\s*\.(input|event)((?:\.[\w-]+)*)\s*\}\}/g,
    (_m, root: string, rest: string) => {
      const path = rest.replace(/^\./, "");
      const base = root === "input" ? data.input : data.event;
      return fieldText(path === "" ? base : lookupPath(base, path));
    },
  );
}

/** The approval a node would raise for `sample` (used as both the input and the event). */
export function previewApproval(
  node: ApprovalNode,
  sample: unknown,
  opts: { dryRun?: boolean } = {},
): ApprovalPreview {
  const data = { input: sample, event: sample };
  const body = renderTemplate(node.body ?? "", data);
  return {
    title: renderTemplate(node.title, data).trim(),
    body_md: opts.dryRun
      ? `${DRY_RUN_NOTICE}\n\n${body}`.replace(/\n+$/, "")
      : body,
    fields: (node.fields ?? []).map((key) => ({
      key,
      value: fieldText(lookupPath(sample, key)),
    })),
    priority: node.priority ?? "normal",
    timeout: node.timeout ?? null,
    on_timeout: node.timeout ? (node.on_timeout ?? "denied") : null,
  };
}

/** Input paths of a sample the fields picker offers (no whole-item or per-element paths). */
export function fieldChoices(sample: unknown): string[] {
  if (sample === undefined) return [];
  return discoverFields(sample)
    .map((f) => f.path)
    .filter((p) => p !== "." && !p.includes("[]"));
}

/** Add or remove `path` in a fields list, keeping order and dropping duplicates. */
export function toggleField(fields: readonly string[], path: string): string[] {
  return fields.includes(path)
    ? fields.filter((f) => f !== path)
    : [...fields, path];
}
