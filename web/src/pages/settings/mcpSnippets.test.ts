import { describe, expect, it } from "vitest";
import type { McpInfo } from "../../api/mcp";
import {
  cliLinkCommandLine,
  clientSnippets,
  shellQuote,
  stdioCommandLine,
  toolTimeoutSeconds,
} from "./mcpSnippets";

const BIN = "/Applications/HAL9001.app/Contents/MacOS/hal9001";

function info(over: Partial<McpInfo["stdio"]> = {}): McpInfo {
  return {
    server_name: "hal9001",
    http_url: "http://localhost:9001/mcp",
    base_url: "http://localhost:9001",
    stdio: {
      command: BIN,
      args: ["mcp", "--url", "http://localhost:9001"],
      ...over,
    },
    cli_link: "~/.local/bin/hal9001",
    auth: {
      localhost_trust: { api: true, admin: true },
      cookie_name: "hal9001_session",
    },
    approval_timeout_seconds: 240,
    tools: [],
  };
}

const texts = (
  i: McpInfo,
  client: Parameters<typeof clientSnippets>[1],
  transport: Parameters<typeof clientSnippets>[2],
  cookie: boolean,
) => clientSnippets(i, client, transport, cookie).map((s) => s.text);

describe("shell quoting", () => {
  it("leaves plain words alone and quotes the rest", () => {
    expect(shellQuote(BIN)).toBe(BIN);
    expect(shellQuote("http://localhost:9001")).toBe("http://localhost:9001");
    expect(shellQuote("/Applications/HAL 9001.app/hal9001")).toBe(
      "'/Applications/HAL 9001.app/hal9001'",
    );
    expect(shellQuote("it's")).toBe(`'it'\\''s'`);
    expect(shellQuote("")).toBe("''");
  });

  it("quotes only the binary of the stdio command when its path has a space", () => {
    const i = info({ command: "/Users/me/My Apps/hal9001" });
    expect(stdioCommandLine(i)).toBe(
      "'/Users/me/My Apps/hal9001' mcp --url http://localhost:9001",
    );
    expect(cliLinkCommandLine(i)).toBe(
      "hal9001 mcp --url http://localhost:9001",
    );
  });
});

describe("tool timeout", () => {
  it("is the approval wait plus a minute", () => {
    expect(toolTimeoutSeconds(info())).toBe(300);
  });
});

describe("Claude Code", () => {
  it("http", () => {
    expect(texts(info(), "claude", "http", false)).toEqual([
      "claude mcp add --transport http hal9001 http://localhost:9001/mcp",
      `claude mcp add-json hal9001 '{"type":"http","url":"http://localhost:9001/mcp","timeout":300000}'`,
    ]);
    expect(texts(info(), "claude", "http", true)).toEqual([
      `claude mcp add --transport http hal9001 http://localhost:9001/mcp --header "Cookie: hal9001_session=<SESSION>"`,
      `claude mcp add-json hal9001 '{"type":"http","url":"http://localhost:9001/mcp","headers":{"Cookie":"hal9001_session=<SESSION>"},"timeout":300000}'`,
    ]);
  });

  it("stdio", () => {
    expect(texts(info(), "claude", "stdio", false)).toEqual([
      `claude mcp add --transport stdio hal9001 -- ${BIN} mcp --url http://localhost:9001`,
      `claude mcp add-json hal9001 '{"type":"stdio","command":"${BIN}","args":["mcp","--url","http://localhost:9001"],"timeout":300000}'`,
    ]);
    expect(texts(info(), "claude", "stdio", true)).toEqual([
      `claude mcp add --env HAL9001_SESSION_COOKIE=<SESSION> --transport stdio hal9001 -- ${BIN} mcp --url http://localhost:9001`,
      `claude mcp add-json hal9001 '{"type":"stdio","command":"${BIN}","args":["mcp","--url","http://localhost:9001"],"env":{"HAL9001_SESSION_COOKIE":"<SESSION>"},"timeout":300000}'`,
    ]);
  });

  it("quotes a binary path with a space", () => {
    const i = info({ command: "/Users/me/My Apps/hal9001" });
    const [add] = texts(i, "claude", "stdio", false);
    expect(add).toBe(
      "claude mcp add --transport stdio hal9001 -- '/Users/me/My Apps/hal9001' mcp --url http://localhost:9001",
    );
  });
});

describe("Codex", () => {
  it("http", () => {
    expect(texts(info(), "codex", "http", false)).toEqual([
      "codex mcp add hal9001 --url http://localhost:9001/mcp",
      `[mcp_servers.hal9001]
url = "http://localhost:9001/mcp"
tool_timeout_sec = 300`,
    ]);
    expect(texts(info(), "codex", "http", true)[1]).toBe(
      `[mcp_servers.hal9001]
url = "http://localhost:9001/mcp"
http_headers = { Cookie = "hal9001_session=<SESSION>" }
tool_timeout_sec = 300`,
    );
  });

  it("stdio", () => {
    expect(texts(info(), "codex", "stdio", false)).toEqual([
      `codex mcp add hal9001 -- ${BIN} mcp --url http://localhost:9001`,
      `[mcp_servers.hal9001]
command = "${BIN}"
args = ["mcp", "--url", "http://localhost:9001"]
tool_timeout_sec = 300`,
    ]);
    expect(texts(info(), "codex", "stdio", true)).toEqual([
      `codex mcp add hal9001 --env HAL9001_SESSION_COOKIE=<SESSION> -- ${BIN} mcp --url http://localhost:9001`,
      `[mcp_servers.hal9001]
command = "${BIN}"
args = ["mcp", "--url", "http://localhost:9001"]
env = { HAL9001_SESSION_COOKIE = "<SESSION>" }
tool_timeout_sec = 300`,
    ]);
  });

  it("puts a path with a space in the shell command quoted and in TOML as is", () => {
    const [cmd, toml] = texts(
      info({ command: "/Users/me/My Apps/hal9001" }),
      "codex",
      "stdio",
      false,
    );
    expect(cmd).toBe(
      "codex mcp add hal9001 -- '/Users/me/My Apps/hal9001' mcp --url http://localhost:9001",
    );
    expect(toml).toContain('command = "/Users/me/My Apps/hal9001"');
  });
});

describe("OMP", () => {
  it("http", () => {
    expect(texts(info(), "omp", "http", false)).toEqual([
      `{
  "mcpServers": {
    "hal9001": {
      "type": "http",
      "url": "http://localhost:9001/mcp",
      "timeout": 300000
    }
  }
}`,
      "/mcp add hal9001 --scope user --url http://localhost:9001/mcp --transport http",
    ]);
    expect(texts(info(), "omp", "http", true)[0]).toBe(`{
  "mcpServers": {
    "hal9001": {
      "type": "http",
      "url": "http://localhost:9001/mcp",
      "headers": {
        "Cookie": "hal9001_session=<SESSION>"
      },
      "timeout": 300000
    }
  }
}`);
  });

  it("stdio", () => {
    expect(texts(info(), "omp", "stdio", true)).toEqual([
      `{
  "mcpServers": {
    "hal9001": {
      "type": "stdio",
      "command": "${BIN}",
      "args": [
        "mcp",
        "--url",
        "http://localhost:9001"
      ],
      "env": {
        "HAL9001_SESSION_COOKIE": "<SESSION>"
      },
      "timeout": 300000
    }
  }
}`,
      `/mcp add hal9001 --scope user -- ${BIN} mcp --url http://localhost:9001`,
    ]);
  });
});

describe("Pi", () => {
  it("http", () => {
    expect(texts(info(), "pi", "http", false)).toEqual([
      "pi mcp add hal9001 --url http://localhost:9001/mcp --exposure direct",
      `{
  "mcpServers": {
    "hal9001": {
      "url": "http://localhost:9001/mcp",
      "exposure": "direct",
      "timeout": 300
    }
  }
}`,
    ]);
    expect(texts(info(), "pi", "http", true)).toEqual([
      "pi mcp add hal9001 --url http://localhost:9001/mcp --exposure direct --header 'Cookie=hal9001_session=<SESSION>'",
      `{
  "mcpServers": {
    "hal9001": {
      "url": "http://localhost:9001/mcp",
      "headers": {
        "Cookie": "hal9001_session=<SESSION>"
      },
      "exposure": "direct",
      "timeout": 300
    }
  }
}`,
    ]);
  });

  it("stdio", () => {
    expect(texts(info(), "pi", "stdio", false)[0]).toBe(
      `pi mcp add hal9001 --exposure direct -- ${BIN} mcp --url http://localhost:9001`,
    );
    expect(texts(info(), "pi", "stdio", true)[0]).toBe(
      `pi mcp add hal9001 --exposure direct --env HAL9001_SESSION_COOKIE=<SESSION> -- ${BIN} mcp --url http://localhost:9001`,
    );
    expect(texts(info(), "pi", "stdio", true)[1]).toBe(`{
  "mcpServers": {
    "hal9001": {
      "command": "${BIN}",
      "args": [
        "mcp",
        "--url",
        "http://localhost:9001"
      ],
      "env": {
        "HAL9001_SESSION_COOKIE": "<SESSION>"
      },
      "exposure": "direct",
      "timeout": 300
    }
  }
}`);
  });
});
