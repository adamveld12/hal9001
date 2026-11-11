import { useState } from "react";
import { useMcpInfo, type McpInfo } from "../../api/mcp";
import { CodeBlock } from "../../components/CodeBlock";
import { Tabs } from "../../components/Tabs";
import {
  Badge,
  ErrorState,
  LoadingState,
  Panel,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList } from "../../lib/contextSummary";
import {
  CLAUDE_SCOPES,
  clientGuide,
  clientSnippets,
  cliLinkCommandLine,
  MCP_CLIENTS,
  MCP_TRANSPORTS,
  SESSION_ENV,
  SESSION_PLACEHOLDER,
  stdioCommandLine,
  cookiePair,
  type McpClient,
  type McpTransport,
} from "./mcpSnippets";

const code = "font-mono text-[11px]";

export function McpSection() {
  const info = useMcpInfo();
  usePageContext(
    info.data
      ? {
          title: "Settings: MCP",
          facts: factList([
            ["tools", info.data.tools.length],
            [
              "tools needing approval",
              info.data.tools
                .filter((t) => t.needs_approval)
                .map((t) => t.name)
                .join(", "),
            ],
            [
              "tools needing admin",
              info.data.tools
                .filter((t) => t.needs_admin)
                .map((t) => t.name)
                .join(", "),
            ],
            ["approval timeout seconds", info.data.approval_timeout_seconds],
          ]),
        }
      : null,
  );
  if (info.isPending) return <LoadingState />;
  if (info.isError) {
    return (
      <ErrorState error={info.error} onRetry={() => void info.refetch()} />
    );
  }
  return <McpDetails info={info.data} />;
}

/** "a", "a and b", "a, b and c". */
function joinNames(names: readonly string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

function McpDetails({ info }: { info: McpInfo }) {
  const approvalTools = info.tools.filter((t) => t.needs_approval);
  const names = joinNames(approvalTools.map((t) => t.name));
  const adminNames = joinNames(
    info.tools.filter((t) => t.needs_admin).map((t) => t.name),
  );
  return (
    <div className="space-y-4">
      <Panel>
        <p className="m-0 text-xs">
          hal9001 is an MCP server for other agents and editors on this Mac.
          Reads run directly.
          {approvalTools.length > 0 && (
            <>
              {" "}
              <span className={code}>{names}</span> raise an approval in the
              Inbox and wait for your decision (the tools that create or run
              things for up to {info.approval_timeout_seconds} s;{" "}
              <span className={code}>request_approval</span> waits at most that
              long per call and the approval stays open).
            </>
          )}{" "}
          Other tools post notes. No tool can approve or deny anything.
        </p>
      </Panel>
      <ConnectionPanel info={info} />
      <AuthPanel info={info} adminNames={adminNames} />
      <ToolsPanel info={info} />
      <InstallPanel info={info} />
    </div>
  );
}

function ConnectionPanel({ info }: { info: McpInfo }) {
  return (
    <Panel title="Connection details">
      <div className="space-y-3 text-xs">
        <div className="space-y-1">
          <div className="font-medium">HTTP endpoint</div>
          <CodeBlock text={info.http_url} />
        </div>
        <div className="space-y-1">
          <div className="font-medium">stdio command</div>
          <CodeBlock text={stdioCommandLine(info)} />
        </div>
        <p className="m-0 text-muted">
          If you ran <b>Install command-line tool</b> in the app,{" "}
          <span className={code}>{info.cli_link}</span> can replace the long
          path: <span className={code}>{cliLinkCommandLine(info)}</span> (the
          client needs its directory on its PATH). The link follows app updates.
          The absolute path above is stable too, but breaks if the app is moved.
        </p>
      </div>
    </Panel>
  );
}

function AuthPanel({
  info,
  adminNames,
}: {
  info: McpInfo;
  adminNames: string;
}) {
  const { api, admin } = info.auth.localhost_trust;
  return (
    <Panel title="Authentication">
      <div className="space-y-2 text-xs">
        {api ? (
          <>
            <p className="m-0">Clients on this Mac need no credentials.</p>
            {!admin && (
              <p className="m-0 text-muted">
                {adminNames || "The tools with side effects"} call endpoints
                that need the admin service, which does not trust this Mac.
                Those tools fail with 401 unless the client sends a session
                cookie (see the lines under each client below).
              </p>
            )}
          </>
        ) : (
          <>
            <p className="m-0">
              The client must send a session cookie. Send{" "}
              <span className={code}>Cookie: {cookiePair(info)}</span> (HTTP) or
              set <span className={code}>{SESSION_ENV}</span> in the environment
              of the stdio command.
            </p>
            <p className="m-0 text-muted">
              Get the session id by logging in as described in docs/auth.md,
              &quot;Logging in from scripts&quot;. hal9001 has no static token.
              The snippets below use the placeholder{" "}
              <span className={code}>{SESSION_PLACEHOLDER}</span>.
            </p>
          </>
        )}
      </div>
    </Panel>
  );
}

function ToolsPanel({ info }: { info: McpInfo }) {
  return (
    <Panel title="Tools">
      <Table>
        <thead>
          <tr>
            <Th>Name</Th>
            <Th>Description</Th>
            <Th className="w-px" />
          </tr>
        </thead>
        <tbody>
          {info.tools.map((t) => (
            <tr key={t.name}>
              <Td className="font-mono">{t.name}</Td>
              <Td className="text-muted">{t.description}</Td>
              <Td>
                {t.needs_approval && (
                  <Badge tone="warn">asks for approval</Badge>
                )}
              </Td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Panel>
  );
}

function Snippets({
  info,
  client,
  transport,
  cookie,
}: {
  info: McpInfo;
  client: McpClient;
  transport: McpTransport;
  cookie: boolean;
}) {
  return (
    <div className="space-y-3">
      {clientSnippets(info, client, transport, cookie).map((s) => (
        <div key={s.title} className="space-y-1">
          <div className="text-xs font-medium">{s.title}</div>
          <CodeBlock text={s.text} />
          {s.note && <p className="m-0 text-[11px] text-muted">{s.note}</p>}
        </div>
      ))}
    </div>
  );
}

function InstallPanel({ info }: { info: McpInfo }) {
  const [client, setClient] = useState<McpClient>("claude");
  const [transport, setTransport] = useState<McpTransport>("http");
  const cookie = !info.auth.localhost_trust.api;
  const guide = clientGuide(client, info.server_name);
  return (
    <Panel title="Install in a client">
      <div className="space-y-3">
        <p className="m-0 text-[11px] text-muted">
          Syntax was read from each tool&apos;s own help and docs; none of the
          commands were run against your configuration. Each snippet sets a tool
          timeout of {info.approval_timeout_seconds + 60} s, longer than the
          approval wait.
        </p>
        <Tabs tabs={MCP_CLIENTS} active={client} onChange={setClient} />
        <Tabs
          tabs={MCP_TRANSPORTS}
          active={transport}
          onChange={setTransport}
        />
        {cookie && (
          <p className="m-0 text-[11px] text-muted">
            Replace <span className={code}>{SESSION_PLACEHOLDER}</span> with
            your session id before running a command.
          </p>
        )}
        <Snippets
          info={info}
          client={client}
          transport={transport}
          cookie={cookie}
        />
        {client === "claude" && (
          <ul className="m-0 list-none space-y-0.5 p-0 text-[11px] text-muted">
            {CLAUDE_SCOPES.map((s) => (
              <li key={s.scope}>
                <span className={code}>{s.scope}</span>: {s.text}
              </li>
            ))}
          </ul>
        )}
        {!cookie && (
          <details>
            <summary className="cursor-pointer text-xs text-muted">
              If a login is needed
            </summary>
            <div className="mt-2 space-y-2">
              <p className="m-0 text-[11px] text-muted">
                The same snippets with a session cookie, for a daemon whose auth
                stack asks for a login. Replace{" "}
                <span className={code}>{SESSION_PLACEHOLDER}</span> with your
                session id.
              </p>
              <Snippets
                info={info}
                client={client}
                transport={transport}
                cookie
              />
            </div>
          </details>
        )}
        <div className="space-y-1">
          <div className="text-xs font-medium">Verify</div>
          <CodeBlock text={guide.verify} />
          <p className="m-0 text-[11px] text-muted">{guide.verifyHint}</p>
        </div>
        <p className="m-0 text-[11px] text-muted">{guide.caption}</p>
      </div>
    </Panel>
  );
}
