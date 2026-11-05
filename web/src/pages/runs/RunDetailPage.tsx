import { useMemo } from "react";
import { useParams, useSearchParams } from "react-router";
import { ApiError } from "../../api/client";
import {
  useEventSensor,
  useFlowRun,
  useFlowRunAgentRuns,
  useFlowVersion,
  type NodeRun,
} from "../../api/runs";
import { Time } from "../../components/Time";
import {
  Badge,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  StatusBadge,
} from "../../components/ui";
import { FlowCanvas } from "../../flow/FlowCanvas";
import { flowFromApi } from "../../flow/apiAdapter";
import { flowToGraph, SENSOR_PREFIX } from "../../flow/graph";
import type { Flow } from "../../flow/model";
import { VISUAL_LABEL, VISUAL_TONE } from "../../flow/overlay";
import {
  buildRunOverlay,
  groupByNode,
  overlayForRuns,
} from "../../flow/runOverlay";
import { durationBetween, shortId } from "../../lib/format";
import { Mono, TextLink } from "./Meta";
import { NodeRunPanel } from "./NodeRunPanel";
import { runLabels } from "./runView";

const NO_RUNS: NodeRun[] = [];

/** Node list shown when no node is selected, and when the graph is unavailable. */
function NodeOverview({
  nodeIds,
  byNode,
  onSelect,
}: {
  nodeIds: readonly string[];
  byNode: ReadonlyMap<string, NodeRun[]>;
  onSelect: (id: string) => void;
}) {
  if (nodeIds.length === 0) {
    return (
      <p className="m-0 p-3 text-xs text-muted">
        No node has started in this run yet.
      </p>
    );
  }
  return (
    <ul className="m-0 list-none divide-y divide-line p-0 text-xs">
      {nodeIds.map((id) => {
        const ov = overlayForRuns(byNode.get(id) ?? NO_RUNS);
        return (
          <li key={id}>
            <button
              type="button"
              onClick={() => onSelect(id)}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-raised"
            >
              <span className="min-w-0 flex-1 truncate font-medium">{id}</span>
              {ov ? (
                <>
                  {ov.detail && (
                    <span className="max-w-40 truncate text-muted">
                      {ov.detail}
                    </span>
                  )}
                  <Badge tone={VISUAL_TONE[ov.visual]}>
                    {VISUAL_LABEL[ov.visual]}
                  </Badge>
                </>
              ) : (
                <span className="text-muted">not run</span>
              )}
            </button>
          </li>
        );
      })}
    </ul>
  );
}

export function RunDetailPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const selectedNode = params.get("node");
  const selectedRun = params.get("run");

  const runQ = useFlowRun(id);
  const flowRun = runQ.data?.flow_run;
  const nodeRuns = runQ.data?.node_runs ?? NO_RUNS;
  const versionQ = useFlowVersion(flowRun?.flow_id, flowRun?.flow_hash);
  const sensorQ = useEventSensor(flowRun?.event_id ?? null);
  const agentRunsQ = useFlowRunAgentRuns(id);

  const flow: Flow | null = useMemo(
    () => (versionQ.data?.flow ? flowFromApi(versionQ.data.flow) : null),
    [versionQ.data],
  );
  const byNode = useMemo(() => groupByNode(nodeRuns), [nodeRuns]);
  const labels = useMemo(() => runLabels(nodeRuns), [nodeRuns]);
  const overlay = useMemo(
    () =>
      flow
        ? buildRunOverlay(flow, nodeRuns, { sensorId: sensorQ.data ?? null })
        : undefined,
    [flow, nodeRuns, sensorQ.data],
  );
  const graph = useMemo(() => {
    if (!flow) return null;
    const g = flowToGraph(flow);
    return {
      edges: g.edges,
      nodes: g.nodes.map((n) => ({ ...n, selected: n.id === selectedNode })),
    };
  }, [flow, selectedNode]);

  if (runQ.isPending) return <LoadingState label="Loading run" />;
  if (runQ.isError || !flowRun) {
    if (runQ.error instanceof ApiError && runQ.error.status === 404) {
      return (
        <EmptyState
          title="Run not found"
          hint="It may have been pruned by retention."
          action={<TextLink to="/timeline">Back to timeline</TextLink>}
        />
      );
    }
    return (
      <ErrorState error={runQ.error} onRetry={() => void runQ.refetch()} />
    );
  }

  const select = (nodeId: string | null, runId?: string) => {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (nodeId === null) next.delete("node");
        else next.set("node", nodeId);
        if (runId === undefined) next.delete("run");
        else next.set("run", runId);
        return next;
      },
      { replace: true },
    );
  };

  const versionPath = `/flows/${flowRun.flow_id}/versions/${flowRun.flow_hash}`;
  const overviewIds = flow ? flow.nodes.map((n) => n.id) : [...byNode.keys()];
  const panelNode =
    selectedNode !== null && !selectedNode.startsWith(SENSOR_PREFIX)
      ? selectedNode
      : null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            Run <Mono>{shortId(flowRun.id)}</Mono>
            <StatusBadge status={flowRun.status} />
          </span>
        }
        subtitle={
          <>
            <TextLink to={`/flows/${flowRun.flow_id}`}>
              {flowRun.flow_id}
            </TextLink>
            {" · "}
            <TextLink to={versionPath}>
              version <Mono>{shortId(flowRun.flow_hash)}</Mono>
            </TextLink>
          </>
        }
        actions={
          <>
            {flowRun.event_id && (
              <TextLink to={`/events/${flowRun.event_id}`}>Event</TextLink>
            )}
            {flowRun.replay_of && (
              <TextLink to={`/runs/${flowRun.replay_of}`}>
                Replay of <Mono>{shortId(flowRun.replay_of)}</Mono>
              </TextLink>
            )}
          </>
        }
      />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-line px-4 py-1.5 text-xs text-muted">
        <Badge tone={flowRun.kind === "test" ? "info" : "muted"}>
          {flowRun.kind}
        </Badge>
        {flowRun.dry_run && <Badge tone="warn">dry run</Badge>}
        <span>
          created <Time ts={flowRun.created_at} />
        </span>
        <span>
          started <Time ts={flowRun.started_at} />
        </span>
        <span>
          ended <Time ts={flowRun.ended_at} />
        </span>
        <span>
          duration{" "}
          {durationBetween(
            flowRun.started_at ?? flowRun.created_at,
            flowRun.ended_at,
          )}
        </span>
        {sensorQ.data && <span>sensor {sensorQ.data}</span>}
        {flowRun.error && (
          <span role="alert" className="text-bad">
            {flowRun.error}
          </span>
        )}
      </div>

      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1">
          {versionQ.isPending && <LoadingState label="Loading flow version" />}
          {versionQ.isError && (
            <ErrorState
              error={versionQ.error}
              onRetry={() => void versionQ.refetch()}
            />
          )}
          {versionQ.isSuccess && !graph && (
            <EmptyState
              title="Flow version does not parse"
              hint="The graph cannot be drawn; node runs are listed on the right."
            />
          )}
          {graph && (
            <FlowCanvas
              nodes={graph.nodes}
              edges={graph.edges}
              readOnly
              overlay={overlay}
              onNodeClick={(nodeId) => {
                if (!nodeId.startsWith(SENSOR_PREFIX)) select(nodeId);
              }}
              onPaneClick={() => select(null)}
            />
          )}
        </div>
        <aside
          aria-label="Node details"
          className="w-96 shrink-0 overflow-y-auto border-l border-line bg-panel"
        >
          {panelNode !== null ? (
            <NodeRunPanel
              nodeId={panelNode}
              runs={byNode.get(panelNode) ?? NO_RUNS}
              labels={labels}
              agentRuns={agentRunsQ.data ?? []}
              selectedRunId={selectedRun}
              onSelectRun={(runId) => select(panelNode, runId)}
              onClose={() => select(null)}
            />
          ) : (
            <>
              <div className="border-b border-line px-3 py-2 text-xs font-semibold">
                Nodes
              </div>
              <NodeOverview
                nodeIds={overviewIds}
                byNode={byNode}
                onSelect={(nodeId) => select(nodeId)}
              />
            </>
          )}
        </aside>
      </div>
    </div>
  );
}
