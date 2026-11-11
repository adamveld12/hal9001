import type { components } from "../api/schema";

/**
 * The compact description of the current page that the assistant gets with
 * every turn (`page_context` of `POST /api/assist/sessions/{id}/messages`).
 * Pages publish a PageContextInput through `usePageContext`; the drawer adds
 * the route and caps the whole thing with `capPageContext` when it sends.
 *
 * It is untrusted data for the agent (names and errors on a page can be
 * written by users, agents or other systems) and must never hold secrets or
 * environment values: publish names, ids, counts and statuses, not content.
 */
export type AssistPageContext = components["schemas"]["AssistPageContext"];
export type PageContextFact = components["schemas"]["AssistPageContextFact"];
export type PageContextEntity =
  components["schemas"]["AssistPageContextEntity"];

/** What a page publishes; the route comes from the router. */
export interface PageContextInput {
  /** The page title as shown ("Flow triage", "Inbox"). */
  title: string;
  /** One or two sentences: what is on the page. */
  summary?: string;
  /** Filters, counts, the selected item, validation errors. */
  facts?: readonly PageContextFact[];
  /** The things the assistant may refer to or open (visible rows, nodes). */
  entities?: readonly PageContextEntity[];
}

/** The server renders at most 4096 bytes; stay under it with room for its framing. */
export const MAX_PAGE_CONTEXT_BYTES = 3072;
export const MAX_ENTITIES = 30;
export const MAX_FACTS = 30;

const MAX_ROUTE = 300;
const MAX_TITLE = 160;
const MAX_SUMMARY = 400;
const MAX_LABEL = 120;
const MAX_ID = 120;
const MAX_DETAIL = 160;
const MAX_FACT_VALUE = 300;

const encoder = new TextEncoder();

/** Byte length of the JSON form, which is what the cap measures. */
export function contextBytes(ctx: AssistPageContext): number {
  return encoder.encode(JSON.stringify(ctx)).length;
}

// Control characters would let a page title break the server's line-based section.
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]+/g;

function clip(s: string, max: number): string {
  const t = s.replace(CONTROL, " ").trim();
  if (t.length <= max) return t;
  // Do not cut a surrogate pair in half.
  let end = max - 1;
  const c = t.charCodeAt(end - 1);
  if (c >= 0xd800 && c <= 0xdbff) end--;
  return t.slice(0, end) + "…";
}

function optional<K extends string>(
  key: K,
  value: string | undefined,
  max: number,
): Partial<Record<K, string>> {
  if (value === undefined) return {};
  const v = clip(value, max);
  return v === "" ? {} : ({ [key]: v } as Record<K, string>);
}

/**
 * Normalise a page's input into the request shape: strings clipped and
 * stripped of control characters, lists limited, and, when the JSON is still
 * over MAX_PAGE_CONTEXT_BYTES, entities and then facts dropped from the end
 * (the summary and title are kept; the summary is cut as a last resort).
 */
export function capPageContext(
  route: string,
  input: PageContextInput,
  maxBytes: number = MAX_PAGE_CONTEXT_BYTES,
): AssistPageContext {
  const entities = (input.entities ?? [])
    .slice(0, MAX_ENTITIES)
    .map<PageContextEntity>((e) => ({
      kind: clip(e.kind, 40),
      ...optional("id", e.id, MAX_ID),
      ...optional("label", e.label, MAX_LABEL),
      ...optional("detail", e.detail, MAX_DETAIL),
    }));
  const facts = (input.facts ?? [])
    .slice(0, MAX_FACTS)
    .map<PageContextFact>((f) => ({
      label: clip(f.label, 60),
      value: clip(f.value, MAX_FACT_VALUE),
    }));
  const ctx: AssistPageContext = {
    route: clip(route, MAX_ROUTE),
    title: clip(input.title, MAX_TITLE),
    ...optional("summary", input.summary, MAX_SUMMARY),
    ...(facts.length > 0 ? { facts } : {}),
    ...(entities.length > 0 ? { entities } : {}),
  };
  while (contextBytes(ctx) > maxBytes && ctx.entities?.length) {
    ctx.entities.pop();
    if (ctx.entities.length === 0) delete ctx.entities;
  }
  while (contextBytes(ctx) > maxBytes && ctx.facts?.length) {
    ctx.facts.pop();
    if (ctx.facts.length === 0) delete ctx.facts;
  }
  if (contextBytes(ctx) > maxBytes && ctx.summary !== undefined) {
    delete ctx.summary;
  }
  return ctx;
}

/**
 * The page that is on screen publishes here. One page owns the slot at a
 * time: a page that mounts takes it over, and the previous page's cleanup
 * (which may run after) must not clear its successor.
 */
export class PageContextRegistry {
  private owner: symbol | null = null;
  private value: PageContextInput | null = null;

  set(owner: symbol, input: PageContextInput): void {
    this.owner = owner;
    this.value = input;
  }

  /** Release the slot if `owner` still holds it. */
  clear(owner: symbol): void {
    if (this.owner !== owner) return;
    this.owner = null;
    this.value = null;
  }

  get(): PageContextInput | null {
    return this.value;
  }
}
