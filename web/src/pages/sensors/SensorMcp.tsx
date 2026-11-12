import { Link } from "react-router";
import type { SensorRun } from "../../api/sensors";
import { Time } from "../../components/Time";
import { Badge } from "../../components/ui";
import { formatDuration } from "../../lib/format";

/** Help under the YAML editor: the sensor file is the only place `mcp_servers` is edited. */
export function SensorMcpHint({ servers }: { servers: readonly string[] }) {
  return (
    <p className="m-0 text-[11px] text-muted">
      MCP servers: list the names of{" "}
      <Link to="/settings/context" className="underline">
        remote MCP servers
      </Link>{" "}
      under <code className="font-mono">mcp_servers:</code> (for example{" "}
      <code className="font-mono">mcp_servers: [notion]</code>). The script
      calls them with{" "}
      <code className="font-mono">
        hal9001 mcp call &lt;server&gt; &lt;tool&gt; --args
        &apos;&lt;json&gt;&apos;
      </code>{" "}
      and never sees their credentials.
      {servers.length > 0 && (
        <>
          {" "}
          This sensor uses:{" "}
          <span className="font-mono text-fg">{servers.join(", ")}</span>.
        </>
      )}
    </p>
  );
}

/** The MCP tool calls a sensor run's script made; renders nothing when there were none. */
export function SensorMcpCalls({ calls }: { calls: SensorRun["mcp_calls"] }) {
  if (!calls || calls.length === 0) return null;
  return (
    <div className="space-y-1">
      <div className="text-[11px] font-medium text-muted">MCP calls</div>
      <ul className="m-0 list-none divide-y divide-line rounded-md border border-line p-0 text-xs">
        {calls.map((c, i) => (
          <li
            key={`${c.at}-${i}`}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 px-2.5 py-1.5"
          >
            <span className="font-mono">
              {c.server}.{c.tool}
            </span>
            <Badge tone={c.ok ? "ok" : "bad"}>{c.ok ? "ok" : "error"}</Badge>
            <span className="tabular-nums text-muted">
              {formatDuration(c.duration_ms)}
            </span>
            <Time ts={c.at} />
            {!c.ok && c.error && (
              <span className="min-w-0 basis-full break-words text-bad">
                {c.error}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
