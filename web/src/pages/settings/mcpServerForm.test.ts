import { describe, expect, it } from "vitest";
import {
  buildCreateBody,
  buildUpdateBody,
  emptyForm,
  fieldsFor,
  formFromServer,
  hasErrors,
  parseScopes,
  urlError,
  validateForm,
  type McpForm,
} from "./mcpServerForm";

const base = (over: Partial<McpForm> = {}): McpForm => ({
  ...emptyForm(),
  name: "notion",
  url: "https://mcp.notion.example/mcp",
  ...over,
});
const create = { editing: false, existing: [] as string[] };

describe("urlError", () => {
  it("accepts https and loopback http", () => {
    expect(urlError("https://example.com/mcp")).toBeUndefined();
    expect(urlError("http://localhost:3000/mcp")).toBeUndefined();
    expect(urlError("http://127.0.0.1:3000")).toBeUndefined();
    expect(urlError("http://[::1]:3000")).toBeUndefined();
  });
  it("rejects what the daemon rejects", () => {
    expect(urlError("")).toMatch(/Enter/);
    expect(urlError("example.com/mcp")).toMatch(/absolute/);
    expect(urlError("http://example.com/mcp")).toMatch(/localhost/);
    expect(urlError("ftp://example.com")).toMatch(/https/);
    expect(urlError("https://user:pw@example.com")).toMatch(/credentials/);
    expect(urlError("https://example.com/#x")).toMatch(/fragment/);
  });
});

describe("validateForm", () => {
  it("accepts a valid default form", () => {
    expect(validateForm(base(), create)).toEqual({});
  });
  it("checks the name on create only", () => {
    expect(validateForm(base({ name: "Notion" }), create).name).toBeDefined();
    expect(validateForm(base({ name: "-x" }), create).name).toBeDefined();
    expect(
      validateForm(base({ name: "a".repeat(33) }), create).name,
    ).toBeDefined();
    expect(
      validateForm(base(), { editing: false, existing: ["notion"] }).name,
    ).toMatch(/exists/);
    expect(
      validateForm(base({ name: "" }), { editing: true, existing: [] }),
    ).toEqual({});
  });
  it("requires a client ID for oauth_client only", () => {
    expect(
      validateForm(base({ auth: "oauth_client" }), create).clientId,
    ).toBeDefined();
    expect(
      validateForm(base({ auth: "oauth_client", clientId: "x" }), create),
    ).toEqual({});
    expect(
      validateForm(base({ auth: "oauth" }), create).clientId,
    ).toBeUndefined();
  });
  it("validates the header name in header mode", () => {
    const h = (header: string) =>
      validateForm(base({ auth: "header", header }), create).header;
    expect(h("")).toBeDefined();
    expect(h("Bad Header")).toMatch(/valid/);
    expect(h("Content-Type")).toMatch(/transport/);
    expect(h("X-Api-Key")).toBeUndefined();
    // Not checked in other modes.
    expect(validateForm(base({ auth: "none", header: "" }), create)).toEqual(
      {},
    );
  });
  it("hasErrors", () => {
    expect(hasErrors({})).toBe(false);
    expect(hasErrors({ url: "x" })).toBe(true);
  });
});

describe("fieldsFor", () => {
  it("lists the inputs per mode", () => {
    expect(fieldsFor("oauth")).toMatchObject({
      scopes: true,
      clientId: false,
      header: false,
    });
    expect(fieldsFor("oauth_client")).toMatchObject({
      clientId: true,
      clientSecret: true,
      scopes: true,
    });
    expect(fieldsFor("header")).toMatchObject({
      header: true,
      apiKey: true,
      scopes: false,
    });
    expect(fieldsFor("none")).toEqual({
      clientId: false,
      clientSecret: false,
      scopes: false,
      header: false,
      apiKey: false,
    });
  });
});

describe("parseScopes", () => {
  it("splits on spaces and commas", () => {
    expect(parseScopes(" read, write  admin ")).toEqual([
      "read",
      "write",
      "admin",
    ]);
    expect(parseScopes("")).toEqual([]);
  });
});

describe("buildCreateBody", () => {
  it("sends only the fields of the mode", () => {
    expect(
      buildCreateBody(
        base({ name: " notion ", url: " https://a.example/mcp " }),
      ),
    ).toEqual({
      name: "notion",
      url: "https://a.example/mcp",
      auth: "oauth",
    });
    expect(
      buildCreateBody(
        base({
          auth: "oauth_client",
          clientId: "id",
          clientSecret: "s3",
          scopes: "a b",
          header: "ignored",
          apiKey: "ignored",
        }),
      ),
    ).toEqual({
      name: "notion",
      url: "https://mcp.notion.example/mcp",
      auth: "oauth_client",
      client_id: "id",
      client_secret: "s3",
      scopes: ["a", "b"],
    });
    expect(
      buildCreateBody(
        base({
          auth: "header",
          header: "X-Key",
          apiKey: "Bearer t",
          clientId: "x",
          scopes: "a",
        }),
      ),
    ).toEqual({
      name: "notion",
      url: "https://mcp.notion.example/mcp",
      auth: "header",
      header: "X-Key",
      api_key: "Bearer t",
    });
  });
  it("never sends an empty secret", () => {
    const b1 = buildCreateBody(base({ auth: "oauth_client", clientId: "id" }));
    expect(b1).not.toHaveProperty("client_secret");
    const b2 = buildCreateBody(base({ auth: "header", header: "X-Key" }));
    expect(b2).not.toHaveProperty("api_key");
  });
});

describe("buildUpdateBody", () => {
  const server = {
    name: "n",
    url: "https://a.example/mcp",
    auth: "header" as const,
    header: "X-Key",
    scopes: [],
    has_client_secret: false,
    has_api_key: true,
    status: "connected" as const,
    tool_count: 0,
  };
  it("keeps a stored secret when the field is blank", () => {
    const body = buildUpdateBody(formFromServer(server));
    expect(body).toEqual({
      url: "https://a.example/mcp",
      auth: "header",
      header: "X-Key",
    });
    expect(body).not.toHaveProperty("api_key");
  });
  it("replaces with a typed value, removes with an empty string", () => {
    expect(
      buildUpdateBody({ ...formFromServer(server), apiKey: "new" }).api_key,
    ).toBe("new");
    expect(
      buildUpdateBody({ ...formFromServer(server), removeApiKey: true })
        .api_key,
    ).toBe("");
  });
  it("a typed value wins over remove", () => {
    expect(
      buildUpdateBody({
        ...formFromServer(server),
        apiKey: "new",
        removeApiKey: true,
      }).api_key,
    ).toBe("new");
  });
  it("sends scopes for OAuth modes so they can be cleared", () => {
    const f = { ...base({ auth: "oauth" }), scopes: "" };
    expect(buildUpdateBody(f).scopes).toEqual([]);
    expect(buildUpdateBody({ ...f, auth: "none" })).not.toHaveProperty(
      "scopes",
    );
  });
  it("round-trips a server into the form", () => {
    const f = formFromServer({
      ...server,
      auth: "oauth_client",
      client_id: "cid",
      header: undefined,
      scopes: ["a", "b"],
      has_client_secret: true,
    });
    expect(f).toMatchObject({
      clientId: "cid",
      scopes: "a b",
      clientSecret: "",
    });
    expect(buildUpdateBody(f)).toEqual({
      url: "https://a.example/mcp",
      auth: "oauth_client",
      client_id: "cid",
      scopes: ["a", "b"],
    });
  });
});
