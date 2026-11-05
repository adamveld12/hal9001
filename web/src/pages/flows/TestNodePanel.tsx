import { useState } from "react";
import { Link } from "react-router";
import { ApiError, errorMessage } from "../../api/client";
import {
  useFlowRunDetail,
  useFlowRuns,
  useRecentEvents,
  useTestNode,
} from "../../api/flows";
import { useNodeRun } from "../../api/runs";
import { BlobView } from "../../components/BlobView";
import {
  Button,
  Field,
  Select,
  StatusBadge,
  TextArea,
  TextInput,
  Toggle,
} from "../../components/ui";
import { flowToApi } from "../../flow/apiAdapter";
import type { Flow, FlowNode } from "../../flow/model";
import { formatDuration, relativeTime, shortId } from "../../lib/format";
import {
  buildSampleInput,
  upstreamNodeIds,
  type SampleSource,
  type SampleState,
} from "./testNode";

const INITIAL: SampleState = {
  source: "json",
  eventId: "",
  nodeRunId: "",
  outputIndex: "",
  json: "{}",
};

/** "Run node on sample": runs the draft node against a chosen input (§8, §11). */
export function TestNodePanel({
  flowId,
  node,
  flow,
}: {
  flowId: string;
  node: FlowNode;
  flow: Flow;
}) {
  const [sample, setSample] = useState<SampleState>(INITIAL);
  const [dryRun, setDryRun] = useState(false);
  const [sensor, setSensor] = useState(flow.subscribe[0] ?? "");
  const [runId, setRunId] = useState("");
  const [pending, setPending] = useState<string>();
  const test = useTestNode(flowId);
  const upstream = upstreamNodeIds(flow.edges, node.id);

  const events = useRecentEvents(
    sensor || undefined,
    sample.source === "event" && sensor !== "",
  );
  const runs = useFlowRuns(flowId, sample.source === "node_run");
  const runDetail = useFlowRunDetail(
    sample.source === "node_run" && runId !== "" ? runId : undefined,
  );
  const upstreamRuns = (runDetail.data?.node_runs ?? []).filter((n) =>
    upstream.includes(n.node_id),
  );

  const live = useNodeRun(pending);
  const result = live.data ?? test.data?.node_run;
  const built = buildSampleInput(sample);

  const set = (patch: Partial<SampleState>) =>
    setSample((s) => ({ ...s, ...patch }));

  const run = () => {
    if (!built.ok) return;
    setPending(undefined);
    test.mutate(
      {
        node_id: node.id,
        node: flowToApi({ ...flow, nodes: [node] }).nodes[0],
        input: built.input,
        dry_run: dryRun,
      },
      { onSuccess: (r) => setPending(r.node_run.id) },
    );
  };

  const err = test.error;
  const paused = err instanceof ApiError && err.kind === "paused";

  return (
    <section className="space-y-2" aria-label="Run node on sample">
      <h3 className="text-xs font-semibold">Run node on sample</h3>
      <Field label="Input">
        <Select
          value={sample.source}
          onChange={(e) => set({ source: e.target.value as SampleSource })}
        >
          <option value="json">Pasted JSON</option>
          <option value="event" disabled={flow.subscribe.length === 0}>
            Stored event
          </option>
          <option value="node_run" disabled={upstream.length === 0}>
            Upstream node output
          </option>
        </Select>
      </Field>

      {sample.source === "json" && (
        <TextArea
          rows={5}
          aria-label="Sample JSON"
          value={sample.json}
          spellCheck={false}
          onChange={(e) => set({ json: e.target.value })}
        />
      )}

      {sample.source === "event" && (
        <>
          <Field label="Sensor">
            <Select value={sensor} onChange={(e) => setSensor(e.target.value)}>
              {flow.subscribe.map((s) => (
                <option key={s}>{s}</option>
              ))}
            </Select>
          </Field>
          <Field
            label="Event"
            error={events.isError ? [errorMessage(events.error)] : undefined}
          >
            <Select
              value={sample.eventId}
              onChange={(e) => set({ eventId: e.target.value })}
            >
              <option value="">
                {events.isPending ? "Loading…" : "(choose an event)"}
              </option>
              {(events.data ?? []).map((ev) => (
                <option key={ev.id} value={ev.id}>
                  {relativeTime(ev.created_at)} ·{" "}
                  {ev.payload_preview.slice(0, 60)}
                </option>
              ))}
            </Select>
          </Field>
          {events.data?.length === 0 && (
            <p className="text-[11px] text-muted">
              No events for this sensor yet.
            </p>
          )}
        </>
      )}

      {sample.source === "node_run" && (
        <>
          <Field
            label="Run of this flow"
            error={runs.isError ? [errorMessage(runs.error)] : undefined}
          >
            <Select value={runId} onChange={(e) => setRunId(e.target.value)}>
              <option value="">
                {runs.isPending ? "Loading…" : "(choose a run)"}
              </option>
              {(runs.data ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {shortId(r.id)} · {r.status} · {relativeTime(r.created_at)}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={`Output of ${upstream.join(", ")}`}>
            <Select
              value={sample.nodeRunId}
              onChange={(e) => set({ nodeRunId: e.target.value })}
            >
              <option value="">
                {runId === "" ? "(choose a run first)" : "(choose a node run)"}
              </option>
              {upstreamRuns.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.node_id} · {n.status} · {n.emitted_count} emitted
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Output index"
            hint="Which payload when the node emitted several."
          >
            <TextInput
              type="number"
              min={0}
              value={sample.outputIndex}
              placeholder="0"
              onChange={(e) => set({ outputIndex: e.target.value })}
            />
          </Field>
        </>
      )}

      {!built.ok && <p className="text-[11px] text-muted">{built.error}</p>}
      <div className="flex items-center justify-between gap-3">
        <Toggle label="Dry run" checked={dryRun} onChange={setDryRun} />
        <Button
          variant="primary"
          icon="play"
          disabled={!built.ok || test.isPending}
          onClick={run}
          title="Runs the node as currently edited, without saving"
        >
          {test.isPending ? "Running…" : "Run node"}
        </Button>
      </div>

      {err && (
        <p role="alert" className="text-[11px] text-bad">
          {paused
            ? "The kill switch is engaged; resume to run nodes."
            : errorMessage(err)}
        </p>
      )}

      {result && (
        <div
          className="space-y-2 rounded-md border border-line p-2"
          data-testid="test-node-result"
        >
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <StatusBadge status={result.status} />
            {result.decision && <StatusBadge status={result.decision} />}
            <span className="text-muted">exit {result.exit_code ?? "–"}</span>
            <span className="text-muted">
              {formatDuration(result.duration_ms)}
            </span>
            <Link
              to={`/runs/${result.flow_run_id}`}
              className="ml-auto text-accent underline"
            >
              open trace
            </Link>
          </div>
          {result.reason && (
            <div className="text-[11px] text-muted">{result.reason}</div>
          )}
          {result.error && (
            <div className="text-[11px] text-bad">{result.error}</div>
          )}
          {result.ports.length > 0 && (
            <div className="text-[11px] text-muted">
              emitted {result.emitted_count} on {result.ports.join(", ")}
            </div>
          )}
          <div>
            <div className="mb-1 text-[11px] font-medium text-muted">
              stdout
            </div>
            <BlobView blob={result.stdout} maxHeight="10rem" />
          </div>
          <div>
            <div className="mb-1 text-[11px] font-medium text-muted">
              stderr
            </div>
            <BlobView blob={result.stderr} maxHeight="8rem" />
          </div>
        </div>
      )}
    </section>
  );
}
