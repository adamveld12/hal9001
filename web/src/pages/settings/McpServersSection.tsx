import { useState } from "react";
import { useSearchParams } from "react-router";
import { useMcpServers } from "../../api/mcpServers";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList, rowEntities } from "../../lib/contextSummary";
import { McpServerCard } from "./McpServerCard";
import { McpServerEditor } from "./McpServerEditor";
import { STATUS_LABEL, serversSummary } from "./mcpServerView";

type Editing = { kind: "add" } | { kind: "edit"; name: string } | null;

/** Settings → Context → MCP servers: remote MCP servers hal9001 connects to as a client. */
export function McpServersSection() {
  const servers = useMcpServers();
  const [editing, setEditing] = useState<Editing>(null);
  const [params, setParams] = useSearchParams();
  // The daemon's OAuth callback redirects here with ?mcp_server=<name>.
  const arrived = params.get("mcp_server");

  const list = servers.data ?? [];
  const arrivedServer = arrived
    ? list.find((s) => s.name === arrived)
    : undefined;
  usePageContext({
    title: "Settings: Context, MCP servers",
    facts: factList([
      ["remote MCP servers", servers.isSuccess ? list.length : undefined],
      ["remote MCP server statuses", serversSummary(list)],
    ]),
    entities: rowEntities(list, (s) => ({
      kind: "mcp_server",
      id: s.name,
      label: s.name,
      detail: `${STATUS_LABEL[s.status]}, ${s.tool_count} tools`,
    })),
  });

  const dismiss = () => {
    const next = new URLSearchParams(params);
    next.delete("mcp_server");
    setParams(next, { replace: true });
  };

  return (
    <Panel
      title="MCP servers"
      actions={
        editing === null &&
        list.length > 0 && (
          <Button icon="plus" onClick={() => setEditing({ kind: "add" })}>
            Add server
          </Button>
        )
      }
    >
      <p className="m-0 mb-3 text-xs text-muted">
        Remote MCP servers hal9001 connects to as a client. Sensors list them in{" "}
        <code className="font-mono">mcp_servers</code> and call their tools with{" "}
        <code className="font-mono">hal9001 mcp call</code>; the chat assistant
        can call them after you approve each call. Credentials stay in the
        daemon.
      </p>

      {arrived && servers.isSuccess && (
        <Notice
          bad={
            arrivedServer?.status === "error" ||
            arrivedServer?.status === "needs_reconnect"
          }
          onDismiss={dismiss}
        >
          {arrivedServer === undefined
            ? `Authorization finished for ${arrived}, but no server has that name.`
            : arrivedServer.status === "error" ||
                arrivedServer.status === "needs_reconnect"
              ? `Authorization for ${arrived} did not complete: ${arrivedServer.error ?? STATUS_LABEL[arrivedServer.status]}`
              : `Authorization finished for ${arrived}.`}
        </Notice>
      )}

      {servers.isPending && <LoadingState />}
      {servers.isError && (
        <ErrorState
          error={servers.error}
          onRetry={() => void servers.refetch()}
        />
      )}

      {editing?.kind === "add" && (
        <div className="mb-3">
          <McpServerEditor
            existing={list.map((s) => s.name)}
            onCancel={() => setEditing(null)}
            onDone={() => setEditing(null)}
          />
        </div>
      )}

      {servers.isSuccess && list.length === 0 && editing === null && (
        <EmptyState
          title="No MCP servers"
          hint="Add a remote MCP server to let sensors and the assistant use its tools."
          action={
            <Button
              variant="primary"
              icon="plus"
              onClick={() => setEditing({ kind: "add" })}
            >
              Add server
            </Button>
          }
        />
      )}

      <div className="space-y-3">
        {list.map((s) =>
          editing?.kind === "edit" && editing.name === s.name ? (
            <McpServerEditor
              key={s.name}
              server={s}
              existing={list.map((x) => x.name)}
              onCancel={() => setEditing(null)}
              onDone={() => setEditing(null)}
            />
          ) : (
            <McpServerCard
              key={s.name}
              server={s}
              highlighted={s.name === arrived}
              onEdit={() => setEditing({ kind: "edit", name: s.name })}
            />
          ),
        )}
      </div>
    </Panel>
  );
}

function Notice({
  bad,
  onDismiss,
  children,
}: {
  bad: boolean;
  onDismiss: () => void;
  children: string;
}) {
  return (
    <div
      role="status"
      className={
        bad
          ? "mb-3 flex items-center gap-2 rounded-md border border-bad/30 bg-bad/5 px-3 py-2 text-xs text-bad"
          : "mb-3 flex items-center gap-2 rounded-md border border-ok/30 bg-ok/5 px-3 py-2 text-xs text-ok"
      }
    >
      <span className="min-w-0 flex-1 break-words">{children}</span>
      <Button variant="ghost" onClick={onDismiss} aria-label="Dismiss notice">
        Dismiss
      </Button>
    </div>
  );
}
