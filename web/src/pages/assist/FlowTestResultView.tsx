import { Link } from "react-router";
import type {
  AssistFlowTestNode,
  AssistFlowTestResult,
} from "../../api/assist";
import { SensorItemsTable } from "../../components/SensorItemsTable";
import { Badge, StatusBadge } from "../../components/ui";
import { formatDuration } from "../../lib/format";

function InboxPreview({
  item,
}: {
  item: NonNullable<AssistFlowTestNode["would_create"]>;
}) {
  return (
    <div
      className="space-y-1 rounded-md border border-info/30 bg-info/5 p-2"
      data-testid="would-create"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="info">would create inbox item</Badge>
        <Badge tone={item.priority === "urgent" ? "bad" : "muted"}>
          {item.priority}
        </Badge>
      </div>
      <div className="text-xs font-medium">{item.title}</div>
      {item.summary_md !== "" && (
        <div className="whitespace-pre-wrap text-muted">{item.summary_md}</div>
      )}
      {item.suggested_reply !== "" && (
        <div>
          <div className="text-[11px] font-medium text-muted">
            Suggested reply
          </div>
          <div className="whitespace-pre-wrap">{item.suggested_reply}</div>
        </div>
      )}
      {item.links.length > 0 && (
        <ul className="m-0 list-none space-y-0.5 p-0">
          {item.links.map((l) => (
            <li key={`${l.label}|${l.url}`}>
              <span className="text-muted">{l.label}: </span>
              <span className="break-all font-mono text-[11px]">{l.url}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Output({ text, label }: { text: string | null; label: string }) {
  if (text === null || text === "") return null;
  return (
    <details className="rounded-md border border-line">
      <summary className="cursor-pointer select-none px-2 py-1">
        {label}
      </summary>
      <pre className="m-0 max-h-40 overflow-auto border-t border-line p-2 font-mono text-[11px] whitespace-pre-wrap">
        {text}
      </pre>
    </details>
  );
}

function NodeCard({ node }: { node: AssistFlowTestNode }) {
  return (
    <li
      className="space-y-2 rounded-md border border-line p-2"
      data-testid={`test-node-${node.node_id}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-xs font-semibold">{node.node_id}</span>
        <StatusBadge status={node.status} />
        {node.decision !== null && <StatusBadge status={node.decision} />}
        {node.exit_code !== null && node.exit_code !== 0 && (
          <span className="text-bad">exit {node.exit_code}</span>
        )}
        <span className="ml-auto text-muted">
          {formatDuration(node.duration_ms)}
        </span>
      </div>
      {node.reason !== null && <div className="text-muted">{node.reason}</div>}
      {node.error !== null && (
        <div role="alert" className="text-bad">
          {node.error}
        </div>
      )}
      {node.ports.length > 0 && (
        <div className="text-muted">
          emitted {node.outputs.length} on {node.ports.join(", ")}
        </div>
      )}
      {node.would_create !== null && <InboxPreview item={node.would_create} />}
      {node.outputs.length > 0 && node.would_create === null && (
        <SensorItemsTable
          items={node.outputs.map((o) => ({ item: o.payload }))}
          notice={
            node.ports.length > 1 || node.outputs.some((o) => o.port !== "out")
              ? `Ports: ${[...new Set(node.outputs.map((o) => o.port))].join(", ")}`
              : undefined
          }
          maxHeight="14rem"
        />
      )}
      <Output text={node.stdout} label="stdout" />
      <Output text={node.stderr} label="stderr" />
    </li>
  );
}

/**
 * Outcome of testing a flow draft on one input: the run's status, what each
 * node that ran did, with its outputs, the inbox items it would create and
 * why a node did not run.
 */
export function FlowTestResultView({
  result,
}: {
  result: AssistFlowTestResult;
}) {
  const errors = result.nodes.filter(
    (n) => n.error !== null || n.status === "failed",
  ).length;
  const skipped = result.nodes.filter((n) => n.status === "skipped").length;
  const wouldCreate = result.nodes.filter(
    (n) => n.would_create !== null,
  ).length;
  return (
    <div className="space-y-3 text-xs">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <StatusBadge status={result.status} />
        <span>
          {result.nodes.length} node{result.nodes.length === 1 ? "" : "s"} ran
        </span>
        {skipped > 0 && <span className="text-muted">{skipped} skipped</span>}
        {errors > 0 && <span className="text-bad">{errors} failed</span>}
        {wouldCreate > 0 && (
          <span className="text-info">
            {wouldCreate} inbox item{wouldCreate === 1 ? "" : "s"} would be
            created
          </span>
        )}
        <Link
          to={`/runs/${result.flow_run_id}`}
          className="ml-auto text-accent underline"
        >
          open trace
        </Link>
      </div>
      <details className="rounded-md border border-line">
        <summary className="cursor-pointer select-none px-2 py-1">
          input
        </summary>
        <pre className="m-0 max-h-40 overflow-auto border-t border-line p-2 font-mono text-[11px]">
          {JSON.stringify(result.input, null, 2)}
        </pre>
      </details>
      {result.nodes.length === 0 ? (
        <div className="text-muted">No node ran.</div>
      ) : (
        <ul className="m-0 list-none space-y-2 p-0">
          {result.nodes.map((n) => (
            <NodeCard key={n.node_run_id} node={n} />
          ))}
        </ul>
      )}
    </div>
  );
}
