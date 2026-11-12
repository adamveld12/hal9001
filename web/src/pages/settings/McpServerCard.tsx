import { useEffect, useRef, useState, type ReactNode } from "react";
import { errorMessage } from "../../api/client";
import {
  useConnectMcpServer,
  useDeleteMcpServer,
  useDisconnectMcpServer,
  useMcpServer,
  useRefreshMcpTools,
  useTestMcpServer,
  type RemoteMcpServer,
} from "../../api/mcpServers";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Time } from "../../components/Time";
import { Badge, Button, ErrorState, Spinner } from "../../components/ui";
import { cx } from "../../lib/cx";
import { formatDuration } from "../../lib/format";
import { McpToolList } from "./McpToolList";
import { authLabel, usesOAuth } from "./mcpServerForm";
import {
  STATUS_LABEL,
  STATUS_TONE,
  changesSummary,
  connectLabel,
  noChanges,
  showsError,
  toolsLabel,
} from "./mcpServerView";

/** One configured remote MCP server: status, actions, and its tool list. */
export function McpServerCard({
  server,
  highlighted,
  onEdit,
}: {
  server: RemoteMcpServer;
  highlighted: boolean;
  onEdit: () => void;
}) {
  const name = server.name;
  const ref = useRef<HTMLElement>(null);
  const [toolsOpen, setToolsOpen] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [confirm, setConfirm] = useState<"remove" | "disconnect" | null>(null);

  const connect = useConnectMcpServer();
  const disconnect = useDisconnectMcpServer();
  const test = useTestMcpServer();
  const refresh = useRefreshMcpTools();
  const remove = useDeleteMcpServer();
  const detail = useMcpServer(name, { enabled: toolsOpen });

  useEffect(() => {
    if (highlighted) {
      ref.current?.scrollIntoView?.({ block: "center", behavior: "smooth" });
    }
  }, [highlighted]);
  // The waiting notice hides itself once the stream event or a window-focus refetch reports `connected`.

  const connectText = connectLabel(server);
  const oauth = usesOAuth(server.auth);
  const busy =
    connect.isPending ||
    disconnect.isPending ||
    test.isPending ||
    refresh.isPending ||
    remove.isPending;
  const doRefresh = () => {
    refresh.reset();
    refresh.mutate(name);
  };

  return (
    <section
      ref={ref}
      aria-label={`MCP server ${name}`}
      data-testid={`mcp-server-${name}`}
      className={cx(
        "rounded-md border bg-panel p-3",
        highlighted ? "border-accent ring-1 ring-accent" : "border-line",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="m-0 font-mono text-sm font-semibold">{name}</h3>
        <Badge tone={STATUS_TONE[server.status]}>
          {STATUS_LABEL[server.status]}
        </Badge>
        <Badge>{authLabel(server.auth)}</Badge>
      </div>
      <div className="mt-1 break-all font-mono text-xs text-muted">
        {server.url}
      </div>
      <dl className="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs">
        {(server.server_name || server.server_version) && (
          <Meta
            label="Server"
            value={[server.server_name, server.server_version]
              .filter(Boolean)
              .join(" ")}
          />
        )}
        {server.transport && (
          <Meta
            label="Transport"
            value={server.transport === "sse" ? "SSE" : "Streamable HTTP"}
          />
        )}
        {server.token_expires_at && server.status === "connected" && (
          <Meta
            label="Token expires"
            value={<Time ts={server.token_expires_at} />}
          />
        )}
        <Meta
          label="Tools"
          value={
            <>
              {toolsLabel(server.tool_count)}
              {server.tools_refreshed_at && (
                <>
                  , refreshed <Time ts={server.tools_refreshed_at} />
                </>
              )}
            </>
          }
        />
      </dl>

      {showsError(server) && (
        <div
          role="status"
          className="mt-2 break-words rounded-md border border-bad/30 bg-bad/5 px-2.5 py-1.5 text-xs text-bad"
        >
          {server.error}
          {server.error_at && (
            <>
              {" "}
              <Time ts={server.error_at} />
            </>
          )}
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        {connectText && (
          <Button
            variant="primary"
            disabled={busy}
            onClick={() => {
              connect.reset();
              connect.mutate(name, {
                onSuccess: (r) => {
                  setWaiting(true);
                  window.open(r.authorize_url, "_blank", "noopener");
                },
              });
            }}
          >
            {connect.isPending ? "Connecting…" : connectText}
          </Button>
        )}
        <Button
          disabled={busy}
          onClick={() => {
            test.reset();
            test.mutate(name);
          }}
        >
          {test.isPending ? "Testing…" : "Test connection"}
        </Button>
        <Button
          disabled={busy}
          icon="refresh"
          onClick={() => {
            setToolsOpen(true);
            doRefresh();
          }}
        >
          {refresh.isPending ? "Refreshing…" : "Refresh tools"}
        </Button>
        {oauth && server.status !== "disconnected" && (
          <Button disabled={busy} onClick={() => setConfirm("disconnect")}>
            Disconnect
          </Button>
        )}
        <Button disabled={busy} onClick={onEdit}>
          Edit
        </Button>
        <Button
          variant="danger"
          disabled={busy}
          onClick={() => {
            remove.reset();
            setConfirm("remove");
          }}
        >
          Remove
        </Button>
      </div>

      {waiting && server.status !== "connected" && (
        <div role="status" className="mt-2 flex items-center gap-2 text-xs">
          <Spinner label="Waiting for authorization in the browser" />
          <Button variant="ghost" onClick={() => setWaiting(false)}>
            Dismiss
          </Button>
        </div>
      )}
      {connect.isError && <Problem>{errorMessage(connect.error)}</Problem>}
      {disconnect.isError && (
        <Problem>{errorMessage(disconnect.error)}</Problem>
      )}
      {remove.isError && !confirm && (
        <Problem>{errorMessage(remove.error)}</Problem>
      )}
      {test.data && (
        <div
          role="status"
          className={cx(
            "mt-2 rounded-md border px-2.5 py-1.5 text-xs",
            test.data.ok
              ? "border-ok/30 bg-ok/5 text-ok"
              : "border-bad/30 bg-bad/5 text-bad",
          )}
        >
          {test.data.ok ? (
            <>
              Connection works: {formatDuration(test.data.latency_ms)}
              {test.data.server_name &&
                `, ${[test.data.server_name, test.data.server_version].filter(Boolean).join(" ")}`}
              , {toolsLabel(test.data.tool_count)}
              {test.data.protocol_version &&
                `, protocol ${test.data.protocol_version}`}
              .
            </>
          ) : (
            <>
              Test failed: {test.data.error ?? STATUS_LABEL[test.data.status]}
            </>
          )}
        </div>
      )}
      {test.isError && <Problem>{errorMessage(test.error)}</Problem>}
      {refresh.data && (
        <div
          role="status"
          className="mt-2 rounded-md border border-line bg-raised px-2.5 py-1.5 text-xs"
        >
          <div>
            Tools refreshed: {changesSummary(refresh.data.changes)}.
            {noChanges(refresh.data.changes) && " The list is up to date."}
          </div>
          <ChangeNames label="Added" names={refresh.data.changes.added} />
          <ChangeNames label="Changed" names={refresh.data.changes.changed} />
          <ChangeNames label="Removed" names={refresh.data.changes.removed} />
        </div>
      )}
      {refresh.isError && <Problem>{errorMessage(refresh.error)}</Problem>}

      <div className="mt-3">
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            aria-expanded={toolsOpen}
            onClick={() => setToolsOpen((o) => !o)}
            className="inline-flex items-center gap-1.5 text-xs font-medium hover:underline"
          >
            <span aria-hidden="true" className="w-3 text-muted">
              {toolsOpen ? "▾" : "▸"}
            </span>
            {toolsLabel(server.tool_count)}
          </button>
          {toolsOpen && (
            <>
              <Button disabled={busy} icon="refresh" onClick={doRefresh}>
                Refresh
              </Button>
              {server.tools_refreshed_at && (
                <span className="text-[11px] text-muted">
                  refreshed <Time ts={server.tools_refreshed_at} />
                </span>
              )}
            </>
          )}
        </div>
        {toolsOpen && (
          <div className="mt-2">
            {detail.isPending && <Spinner label="Loading tools" />}
            {detail.isError && (
              <ErrorState
                error={detail.error}
                onRetry={() => void detail.refetch()}
              />
            )}
            {detail.isSuccess && <McpToolList tools={detail.data.tools} />}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirm === "disconnect"}
        title={`Disconnect ${name}?`}
        confirmLabel="Disconnect"
        busy={disconnect.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          setWaiting(false);
          disconnect.mutate(name, { onSettled: () => setConfirm(null) });
        }}
      >
        The stored OAuth tokens are deleted. Sensors and chats that use this
        server fail until you connect it again.
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === "remove"}
        title={`Remove ${name}?`}
        confirmLabel="Remove server"
        busy={remove.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={() =>
          remove.mutate(name, {
            onSuccess: () => setConfirm(null),
          })
        }
      >
        The server, its stored secrets and tokens are deleted.
        {remove.isError && (
          <div role="alert" className="mt-2 break-words text-bad">
            {errorMessage(remove.error)}
          </div>
        )}
      </ConfirmDialog>
    </section>
  );
}

function Meta({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex gap-1.5">
      <dt className="text-muted">{label}</dt>
      <dd className="m-0">{value}</dd>
    </div>
  );
}

function Problem({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="mt-2 break-words text-xs text-bad">
      {children}
    </div>
  );
}

function ChangeNames({
  label,
  names,
}: {
  label: string;
  names: readonly string[];
}) {
  if (names.length === 0) return null;
  return (
    <div className="mt-0.5 text-muted">
      {label}: <span className="font-mono">{names.join(", ")}</span>
    </div>
  );
}
