import type { McpInfo } from "../../api/mcp";

export type McpClient = "claude" | "codex" | "omp" | "pi";
export type McpTransport = "http" | "stdio";

export const MCP_CLIENTS: readonly { key: McpClient; label: string }[] = [
  { key: "claude", label: "Claude Code" },
  { key: "codex", label: "Codex" },
  { key: "omp", label: "OMP (Oh My Pi)" },
  { key: "pi", label: "Pi" },
];

export const MCP_TRANSPORTS: readonly { key: McpTransport; label: string }[] = [
  { key: "http", label: "HTTP" },
  { key: "stdio", label: "stdio" },
];

/** Placeholder for a session id; never a real one. */
export const SESSION_PLACEHOLDER = "<SESSION>";

/** Environment variable the `hal9001 mcp` command reads the session cookie from. */
export const SESSION_ENV = "HAL9001_SESSION_COOKIE";

export interface Snippet {
  title: string;
  lang: "sh" | "json" | "toml";
  text: string;
  /** Shown under the block. */
  note?: string;
}

export interface ClientGuide {
  /** Which tool versions and sources the syntax was read from. */
  caption: string;
  /** Command or slash command that lists the server. */
  verify: string;
  verifyHint: string;
}

const SAFE_SHELL_WORD = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** Quote for a POSIX shell, only when the word needs it. */
export function shellQuote(word: string): string {
  if (SAFE_SHELL_WORD.test(word)) return word;
  return `'${word.replaceAll("'", `'\\''`)}'`;
}

/** The stdio command as one shell line: the binary (quoted if needed), then its arguments. */
export function stdioCommandLine(info: McpInfo): string {
  return [info.stdio.command, ...info.stdio.args].map(shellQuote).join(" ");
}

/** The same command with the app's command-line link instead of the binary path. */
export function cliLinkCommandLine(info: McpInfo): string {
  return ["hal9001", ...info.stdio.args].map(shellQuote).join(" ");
}

/** Seconds a client must wait for a tool: the approval wait plus a minute. */
export function toolTimeoutSeconds(info: McpInfo): number {
  return info.approval_timeout_seconds + 60;
}

/** Value of the `Cookie` request header carrying the session. */
export function cookiePair(info: McpInfo): string {
  return `${info.auth.cookie_name}=${SESSION_PLACEHOLDER}`;
}

const json = (v: unknown, pretty = false) =>
  pretty ? JSON.stringify(v, null, 2) : JSON.stringify(v);

/** A TOML key: bare when it can be, quoted otherwise. */
function tomlKey(k: string): string {
  return /^[A-Za-z0-9_-]+$/.test(k) ? k : JSON.stringify(k);
}

function tomlBlock(info: McpInfo, transport: McpTransport, cookie: boolean) {
  const lines = [`[mcp_servers.${tomlKey(info.server_name)}]`];
  if (transport === "http") {
    lines.push(`url = ${json(info.http_url)}`);
    if (cookie) {
      lines.push(`http_headers = { Cookie = ${json(cookiePair(info))} }`);
    }
  } else {
    lines.push(`command = ${json(info.stdio.command)}`);
    lines.push(`args = [${info.stdio.args.map((a) => json(a)).join(", ")}]`);
    if (cookie) {
      lines.push(`env = { ${SESSION_ENV} = ${json(SESSION_PLACEHOLDER)} }`);
    }
  }
  lines.push(`tool_timeout_sec = ${toolTimeoutSeconds(info)}`);
  return lines.join("\n");
}

/** `{ "mcpServers": { <name>: server } }`, the shape of OMP's and Pi's `mcp.json`. */
function mcpJson(info: McpInfo, server: Record<string, unknown>): string {
  return json({ mcpServers: { [info.server_name]: server } }, true);
}

function claudeSnippets(
  info: McpInfo,
  transport: McpTransport,
  cookie: boolean,
): Snippet[] {
  const name = info.server_name;
  const ms = toolTimeoutSeconds(info) * 1000;
  const cmd = stdioCommandLine(info);
  const add =
    transport === "http"
      ? `claude mcp add --transport http ${name} ${info.http_url}` +
        (cookie ? ` --header "Cookie: ${cookiePair(info)}"` : "")
      : `claude mcp add ${cookie ? `--env ${SESSION_ENV}=${SESSION_PLACEHOLDER} ` : ""}--transport stdio ${name} -- ${cmd}`;
  const config =
    transport === "http"
      ? {
          type: "http",
          url: info.http_url,
          ...(cookie ? { headers: { Cookie: cookiePair(info) } } : {}),
          timeout: ms,
        }
      : {
          type: "stdio",
          command: info.stdio.command,
          args: info.stdio.args,
          ...(cookie ? { env: { [SESSION_ENV]: SESSION_PLACEHOLDER } } : {}),
          timeout: ms,
        };
  return [
    {
      title: "Add the server",
      lang: "sh",
      text: add,
      note:
        transport === "http"
          ? "Keep the name and URL before --header: the flag takes several values."
          : "Keep --env away from the name: the flag takes several values.",
    },
    {
      title: "Or with an explicit tool timeout",
      lang: "sh",
      text: `claude mcp add-json ${name} ${shellQuote(json(config))}`,
      note: `Claude Code has no short default timeout, so the first command is enough. It aborts an HTTP call that is silent for 5 minutes, which is why hal9001 waits at most ${info.approval_timeout_seconds} s for an approval. The timeout is in milliseconds.`,
    },
  ];
}

function codexSnippets(
  info: McpInfo,
  transport: McpTransport,
  cookie: boolean,
): Snippet[] {
  const name = info.server_name;
  const cmd =
    transport === "http"
      ? `codex mcp add ${name} --url ${info.http_url}`
      : `codex mcp add ${name} ${cookie ? `--env ${SESSION_ENV}=${SESSION_PLACEHOLDER} ` : ""}-- ${stdioCommandLine(info)}`;
  return [
    {
      title: "Add the server",
      lang: "sh",
      text: cmd,
      note: `Codex waits 60 s for a tool by default and this command has no timeout${transport === "http" && cookie ? " or header" : ""} option. Write the config.toml block below instead when you need ${transport === "http" && cookie ? "the header or " : ""}the timeout.`,
    },
    {
      title: "~/.codex/config.toml",
      lang: "toml",
      text: tomlBlock(info, transport, cookie),
      note: "Use this block in place of the command, not after it.",
    },
  ];
}

function ompSnippets(
  info: McpInfo,
  transport: McpTransport,
  cookie: boolean,
): Snippet[] {
  const name = info.server_name;
  const ms = toolTimeoutSeconds(info) * 1000;
  const server =
    transport === "http"
      ? {
          type: "http",
          url: info.http_url,
          ...(cookie ? { headers: { Cookie: cookiePair(info) } } : {}),
          timeout: ms,
        }
      : {
          type: "stdio",
          command: info.stdio.command,
          args: info.stdio.args,
          ...(cookie ? { env: { [SESSION_ENV]: SESSION_PLACEHOLDER } } : {}),
          timeout: ms,
        };
  return [
    {
      title: "~/.omp/agent/mcp.json",
      lang: "json",
      text: mcpJson(info, server),
      note: "Use .omp/mcp.json for one project. The timeout is in milliseconds; omp's default is 30 s and progress notifications do not extend it. omp has no mcp shell command.",
    },
    {
      title: "Or inside an omp session, then edit the file",
      lang: "sh",
      text:
        transport === "http"
          ? `/mcp add ${name} --scope user --url ${info.http_url} --transport http`
          : `/mcp add ${name} --scope user -- ${stdioCommandLine(info)}`,
      note: "This cannot set a header, environment variable or timeout. Add them in the JSON, then run /mcp reload.",
    },
  ];
}

function piSnippets(
  info: McpInfo,
  transport: McpTransport,
  cookie: boolean,
): Snippet[] {
  const name = info.server_name;
  const add =
    transport === "http"
      ? `pi mcp add ${name} --url ${info.http_url} --exposure direct` +
        (cookie ? ` --header 'Cookie=${cookiePair(info)}'` : "")
      : `pi mcp add ${name} --exposure direct ${cookie ? `--env ${SESSION_ENV}=${SESSION_PLACEHOLDER} ` : ""}-- ${stdioCommandLine(info)}`;
  const server =
    transport === "http"
      ? {
          url: info.http_url,
          ...(cookie ? { headers: { Cookie: cookiePair(info) } } : {}),
          exposure: "direct",
          timeout: toolTimeoutSeconds(info),
        }
      : {
          command: info.stdio.command,
          args: info.stdio.args,
          ...(cookie ? { env: { [SESSION_ENV]: SESSION_PLACEHOLDER } } : {}),
          exposure: "direct",
          timeout: toolTimeoutSeconds(info),
        };
  return [
    {
      title: "Add the server",
      lang: "sh",
      text: add,
      note:
        "--exposure direct is required: the default, codemode, hides the tools from the model. Add -l to write a project-local .pi/mcp.json (trusted projects only) instead of ~/.pi/agent/mcp.json." +
        (transport === "http" && cookie
          ? " --header takes KEY=VALUE, not Name: value."
          : ""),
    },
    {
      title: "~/.pi/agent/mcp.json, with the tool timeout",
      lang: "json",
      text: mcpJson(info, server),
      note: "The command has no timeout option. The timeout is in seconds; the default is 60.",
    },
  ];
}

/**
 * Copy-paste install snippets for one client and transport, from the live
 * connection details. `cookie` adds the session placeholder for a daemon whose
 * auth stack asks for a login.
 */
export function clientSnippets(
  info: McpInfo,
  client: McpClient,
  transport: McpTransport,
  cookie: boolean,
): Snippet[] {
  switch (client) {
    case "claude":
      return claudeSnippets(info, transport, cookie);
    case "codex":
      return codexSnippets(info, transport, cookie);
    case "omp":
      return ompSnippets(info, transport, cookie);
    case "pi":
      return piSnippets(info, transport, cookie);
  }
}

/** Claude Code scopes for `claude mcp add --scope`. */
export const CLAUDE_SCOPES: readonly { scope: string; text: string }[] = [
  { scope: "local", text: "this project only, private to you (default)" },
  { scope: "user", text: "all your projects: add --scope user" },
  {
    scope: "project",
    text: "shared with the team in a .mcp.json: add --scope project",
  },
];

const guides = (name: string): Record<McpClient, ClientGuide> => ({
  claude: {
    caption:
      "Checked against Claude Code 2.1.311 (claude mcp add --help and the docs).",
    verify: "claude mcp list",
    verifyHint: "Then /mcp inside a session.",
  },
  codex: {
    caption:
      "Checked against Codex 0.162.1 (codex mcp --help and the config reference).",
    verify: `codex mcp get ${name}`,
    verifyHint: "Also codex mcp list, or /mcp in the TUI.",
  },
  omp: {
    caption:
      "Checked against OMP 18.18.5 (omp --help and docs mcp-config.md). The /mcp add flags come from the binary's usage text and were not run.",
    verify: `/mcp test ${name}`,
    verifyHint: "Also /mcp list, inside an omp session.",
  },
  pi: {
    caption:
      "Checked against Pi 1.5.0 (pi mcp add --help and the installed docs). MCP is built in. A third-party MCP adapter that registers /mcp replaces the built-in support and ignores mcp.json.",
    verify: "pi mcp list",
    verifyHint:
      "It connects and lists the tools. In a session they appear as mcp__hal9001__<tool>.",
  },
});

/** Caption and verify command for a client; `name` is the server name. */
export function clientGuide(client: McpClient, name: string): ClientGuide {
  return guides(name)[client];
}
