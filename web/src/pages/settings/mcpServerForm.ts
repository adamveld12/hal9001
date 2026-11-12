import type { Schemas } from "../../api/client";

type Auth = Schemas["RemoteMcpAuthMode"];
type Server = Schemas["RemoteMcpServer"];
type CreateBody = Schemas["RemoteMcpServerCreateRequest"];
type UpdateBody = Schemas["RemoteMcpServerUpdateRequest"];

/** Same rules as `internal/spec/mcpremote.go`; the daemon validates again. */
export const NAME_PATTERN = /^[a-z0-9][a-z0-9_-]{0,31}$/;
const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const FORBIDDEN_HEADERS: Record<string, true> = {
  host: true,
  "content-length": true,
  "content-type": true,
  accept: true,
  connection: true,
  "transfer-encoding": true,
  "mcp-session-id": true,
  "mcp-protocol-version": true,
};

export const AUTH_MODES: readonly {
  key: Auth;
  label: string;
  hint: string;
}[] = [
  {
    key: "oauth",
    label: "OAuth, register automatically",
    hint: "hal9001 registers itself with the server (dynamic client registration), then you authorize in the browser.",
  },
  {
    key: "oauth_client",
    label: "OAuth with a client ID and secret",
    hint: "For servers without automatic registration: paste the client ID and secret from the provider.",
  },
  {
    key: "header",
    label: "API key or custom header",
    hint: "One secret header is sent with every request.",
  },
  {
    key: "none",
    label: "No authentication",
    hint: "The server needs no credentials.",
  },
];

export function authLabel(auth: Auth): string {
  return AUTH_MODES.find((m) => m.key === auth)?.label ?? auth;
}

export function usesOAuth(auth: Auth): boolean {
  return auth === "oauth" || auth === "oauth_client";
}

/** Which inputs apply to an auth mode. */
export function fieldsFor(auth: Auth): {
  clientId: boolean;
  clientSecret: boolean;
  scopes: boolean;
  header: boolean;
  apiKey: boolean;
} {
  return {
    clientId: auth === "oauth_client",
    clientSecret: auth === "oauth_client",
    scopes: usesOAuth(auth),
    header: auth === "header",
    apiKey: auth === "header",
  };
}

export interface McpForm {
  name: string;
  url: string;
  auth: Auth;
  clientId: string;
  clientSecret: string;
  /** Space or comma separated. */
  scopes: string;
  header: string;
  apiKey: string;
  removeClientSecret: boolean;
  removeApiKey: boolean;
}

export function emptyForm(): McpForm {
  return {
    name: "",
    url: "",
    auth: "oauth",
    clientId: "",
    clientSecret: "",
    scopes: "",
    header: "",
    apiKey: "",
    removeClientSecret: false,
    removeApiKey: false,
  };
}

export function formFromServer(s: Server): McpForm {
  return {
    ...emptyForm(),
    name: s.name,
    url: s.url,
    auth: s.auth,
    clientId: s.client_id ?? "",
    scopes: s.scopes.join(" "),
    header: s.header ?? "",
  };
}

export function parseScopes(text: string): string[] {
  return text.split(/[\s,]+/).filter((s) => s !== "");
}

export type McpFormErrors = Partial<
  Record<"name" | "url" | "clientId" | "header" | "scopes", string>
>;

/** Why `raw` is not an acceptable server URL, or undefined. */
export function urlError(raw: string): string | undefined {
  const v = raw.trim();
  if (v === "") return "Enter the server URL.";
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return "Must be an absolute http(s) URL.";
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") {
    return "Must be an https URL.";
  }
  if (u.username !== "" || u.password !== "") {
    return "Must not contain credentials; use an authentication mode instead.";
  }
  if (u.hash !== "") return "Must not contain a fragment.";
  if (u.protocol === "http:") {
    const h = u.hostname.replace(/^\[|\]$/g, "");
    if (!(h === "localhost" || h === "::1" || h.startsWith("127."))) {
      return "Must use https (http is only allowed for localhost).";
    }
  }
  return undefined;
}

export function nameError(
  name: string,
  existing: readonly string[],
): string | undefined {
  if (name === "") return "Enter a name.";
  if (!NAME_PATTERN.test(name)) {
    return "Use lowercase letters, digits, - and _ (max 32, starting with a letter or digit).";
  }
  if (existing.includes(name)) return `A server named "${name}" exists.`;
  return undefined;
}

/**
 * Client-side validation. `editing` skips the name check (the name is fixed);
 * `existing` are the names already configured.
 */
export function validateForm(
  f: McpForm,
  opts: { editing: boolean; existing: readonly string[] },
): McpFormErrors {
  const errors: McpFormErrors = {};
  if (!opts.editing) {
    const e = nameError(f.name, opts.existing);
    if (e) errors.name = e;
  }
  const u = urlError(f.url);
  if (u) errors.url = u;
  const applies = fieldsFor(f.auth);
  if (applies.clientId && f.clientId.trim() === "") {
    errors.clientId = "Enter the OAuth client ID.";
  }
  if (applies.header) {
    const h = f.header.trim();
    if (h === "") errors.header = "Enter the header name.";
    else if (!HEADER_NAME.test(h)) {
      errors.header = `"${h}" is not a valid HTTP header name.`;
    } else if (Object.hasOwn(FORBIDDEN_HEADERS, h.toLowerCase())) {
      errors.header = `"${h}" is set by the MCP transport and cannot carry a key.`;
    }
  }
  if (applies.scopes) {
    const bad = parseScopes(f.scopes).find((s) => /["\\]/.test(s));
    if (bad) errors.scopes = `Scope ${bad} contains a quote or backslash.`;
  }
  return errors;
}

export function hasErrors(e: McpFormErrors): boolean {
  return Object.keys(e).length > 0;
}

/** The create request: only fields that apply to the mode, and a secret only when typed. */
export function buildCreateBody(f: McpForm): CreateBody {
  const applies = fieldsFor(f.auth);
  const body: CreateBody = {
    name: f.name.trim(),
    url: f.url.trim(),
    auth: f.auth,
  };
  if (applies.clientId) body.client_id = f.clientId.trim();
  if (applies.clientSecret && f.clientSecret !== "") {
    body.client_secret = f.clientSecret;
  }
  if (applies.header) body.header = f.header.trim();
  if (applies.apiKey && f.apiKey !== "") body.api_key = f.apiKey;
  const scopes = applies.scopes ? parseScopes(f.scopes) : [];
  if (scopes.length > 0) body.scopes = scopes;
  return body;
}

/**
 * The update request. A secret is omitted (kept) unless a new value was typed
 * or removal was chosen, in which case it is "". Scopes are always sent for
 * the OAuth modes so clearing the box clears them.
 */
export function buildUpdateBody(f: McpForm): UpdateBody {
  const applies = fieldsFor(f.auth);
  const body: UpdateBody = { url: f.url.trim(), auth: f.auth };
  if (applies.clientId) body.client_id = f.clientId.trim();
  if (applies.clientSecret) {
    if (f.clientSecret !== "") body.client_secret = f.clientSecret;
    else if (f.removeClientSecret) body.client_secret = "";
  }
  if (applies.header) body.header = f.header.trim();
  if (applies.apiKey) {
    if (f.apiKey !== "") body.api_key = f.apiKey;
    else if (f.removeApiKey) body.api_key = "";
  }
  if (applies.scopes) body.scopes = parseScopes(f.scopes);
  return body;
}
