import { useMemo, type ReactNode } from "react";
import { useParams } from "react-router";
import { ApiError } from "../../api/client";
import { useAgentRun, useAgentTranscript } from "../../api/runs";
import { CodeEditor } from "../../components/CodeEditor";
import { CopyButton } from "../../components/CopyButton";
import { JsonView } from "../../components/JsonView";
import { Markdown } from "../../components/Markdown";
import { Time } from "../../components/Time";
import {
  Badge,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Panel,
  Spinner,
  StatusBadge,
} from "../../components/ui";
import { useNow } from "../approvals/timeLeft";
import { durationBetween, formatBytes, shortId } from "../../lib/format";
import { Meta, Mono, TextLink } from "./Meta";
import { PermissionRequests } from "./PermissionRequests";
import { PhaseTimeline } from "./PhaseTimeline";
import { TranscriptView } from "./TranscriptView";
import { SandboxLogSection } from "./SandboxLog";
import { buildTranscript } from "./transcript";

function Transcript({ runId, active }: { runId: string; active: boolean }) {
  const q = useAgentTranscript(runId);
  const items = useMemo(() => buildTranscript(q.data ?? []), [q.data]);
  if (q.isPending) return <LoadingState label="Loading transcript" />;
  if (q.isError)
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (items.length === 0) {
    return (
      <EmptyState
        title="No transcript yet"
        hint={
          active
            ? "Events appear here as the agent works."
            : "The agent produced no events."
        }
      />
    );
  }
  return (
    <div className="space-y-2">
      <TranscriptView items={items} />
      {active && (
        <div className="px-1 text-xs">
          <Spinner label="Agent is working" />
        </div>
      )}
    </div>
  );
}

function Details({
  summary,
  children,
}: {
  summary: ReactNode;
  children: ReactNode;
}) {
  return (
    <details className="rounded-md border border-line bg-panel text-xs">
      <summary className="cursor-pointer select-none px-3 py-2 font-semibold">
        {summary}
      </summary>
      <div className="space-y-2 border-t border-line p-3">{children}</div>
    </details>
  );
}

export function AgentRunPage() {
  const { id = "" } = useParams();
  const q = useAgentRun(id);
  const now = useNow();

  if (q.isPending) return <LoadingState label="Loading agent run" />;
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404) {
      return (
        <EmptyState
          title="Agent run not found"
          hint="It may have been pruned by retention."
          action={<TextLink to="/timeline">Back to timeline</TextLink>}
        />
      );
    }
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  }

  const run = q.data;
  const active =
    run.status === "queued" ||
    run.status === "running" ||
    run.status === "awaiting_approval";
  const info = run.agent_info;
  const flowRunPath =
    run.flow_run_id && run.node_id
      ? `/runs/${run.flow_run_id}?node=${encodeURIComponent(run.node_id)}&run=${run.node_run_id}`
      : null;

  return (
    <div>
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            Agent run <Mono>{shortId(run.id)}</Mono>
            <StatusBadge status={run.status} />
          </span>
        }
        subtitle={
          <>
            {run.runtime}
            {run.flow_id ? ` · ${run.flow_id} / ${run.node_id}` : ""}
          </>
        }
        actions={
          <>
            {flowRunPath && run.flow_run_id && (
              <TextLink to={flowRunPath}>
                Flow run <Mono>{shortId(run.flow_run_id)}</Mono>
              </TextLink>
            )}
            {run.assist_session_id && (
              <TextLink to={`/sensors?assist=${run.assist_session_id}`}>
                Assist session <Mono>{shortId(run.assist_session_id)}</Mono>
              </TextLink>
            )}
            {run.raw_size > 0 ? (
              <a
                href={`/api/agent-runs/${run.id}/raw`}
                download={`agent-run-${run.id}.jsonl`}
                className="text-accent hover:underline"
              >
                Raw JSON-RPC ({formatBytes(run.raw_size)})
              </a>
            ) : (
              <span className="text-xs text-muted">No raw log</span>
            )}
          </>
        }
      />
      <div className="grid gap-4 p-4 xl:grid-cols-[minmax(0,1fr)_26rem]">
        <div className="min-w-0 space-y-4">
          <Panel title="Transcript">
            <Transcript runId={run.id} active={active} />
          </Panel>
          <SandboxLogSection scope="agent" id={run.id} variant="panel" />
          <Panel title="Permission requests">
            <PermissionRequests agentRunId={run.id} />
          </Panel>
        </div>

        <div className="min-w-0 space-y-4">
          <Panel title="Run">
            <Meta
              rows={[
                { label: "Runtime", value: run.runtime },
                {
                  label: "Agent",
                  value: info
                    ? [info.title ?? info.name, info.version]
                        .filter(Boolean)
                        .join(" ")
                    : null,
                },
                { label: "Mode", value: run.mode },
                { label: "Approve style", value: run.approve_style },
                { label: "Policy", value: run.policy },
                { label: "Phase", value: run.phase },
                {
                  label: "Resumed via",
                  value: run.resumed_via
                    ? run.resumed_via.replace("_", " ")
                    : null,
                },
                { label: "Resume fallback", value: run.resume_fallback },
                {
                  label: "Session",
                  value: run.session_id ? <Mono>{run.session_id}</Mono> : null,
                },
                {
                  label: "Working dir",
                  value: run.cwd ? <Mono>{run.cwd}</Mono> : null,
                },
                { label: "Stop reason", value: run.stop_reason },
                {
                  label: "Error",
                  value: run.error ? (
                    <span className="text-bad">{run.error}</span>
                  ) : null,
                },
                { label: "Started", value: <Time ts={run.started_at} /> },
                { label: "Ended", value: <Time ts={run.ended_at} /> },
                {
                  label: "Duration",
                  value: durationBetween(
                    run.started_at ?? run.created_at,
                    run.ended_at,
                    now,
                  ),
                },
              ]}
            />
          </Panel>

          <Panel title="Phases">
            <PhaseTimeline
              phaseRunIds={
                run.phase_run_ids.length > 0 ? run.phase_run_ids : [run.id]
              }
              currentRunId={run.id}
            />
          </Panel>

          {run.approvals.length > 0 && (
            <Panel title="Approvals">
              <ul className="m-0 list-none space-y-1.5 p-0 text-xs">
                {run.approvals.map((a) => (
                  <li key={a.id} className="flex flex-wrap items-center gap-2">
                    <TextLink to={`/approvals/${a.id}`}>
                      <Mono>{shortId(a.id)}</Mono>
                    </TextLink>
                    <Badge>{a.type.replace("_", " ")}</Badge>
                    <StatusBadge status={a.status} />
                    {a.decided_by && (
                      <span className="text-muted">by {a.decided_by}</span>
                    )}
                    <span className="ml-auto">
                      <Time ts={a.decided_at ?? a.created_at} />
                    </span>
                    {a.comment && (
                      <div className="w-full text-muted">{a.comment}</div>
                    )}
                  </li>
                ))}
              </ul>
            </Panel>
          )}

          {(run.final_message ||
            run.result ||
            run.output != null ||
            run.emissions.length > 0 ||
            run.parse_error) && (
            <Panel
              title="Result"
              actions={
                run.final_message ? (
                  <CopyButton text={run.final_message} label="Copy message" />
                ) : undefined
              }
            >
              <div className="space-y-3">
                {run.parse_error && (
                  <Badge tone="warn">
                    final message did not parse as a note
                  </Badge>
                )}
                {run.output != null && (
                  <div>
                    <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">
                      Submitted output (submit_output)
                    </div>
                    <JsonView value={run.output} />
                  </div>
                )}
                {run.emissions.length > 0 && (
                  <div className="space-y-2">
                    <div className="text-[11px] font-semibold uppercase tracking-wide text-muted">
                      Emissions (emit tools)
                    </div>
                    {run.emissions.map((e, i) => (
                      <div key={i}>
                        <div className="mb-1 font-mono text-[11px]">
                          {i + 1}. port <Badge tone="info">{e.port}</Badge>
                        </div>
                        <JsonView value={e.output} />
                      </div>
                    ))}
                  </div>
                )}
                {run.result && <JsonView value={run.result} />}
                {run.final_message && (
                  <div>
                    <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">
                      Final message
                    </div>
                    <Markdown>{run.final_message}</Markdown>
                  </div>
                )}
              </div>
            </Panel>
          )}

          <Details summary="Rendered prompt">
            {run.rendered_prompt === "" ? (
              <div className="text-muted">(empty)</div>
            ) : (
              <>
                <div className="flex justify-end">
                  <CopyButton text={run.rendered_prompt} />
                </div>
                <CodeEditor
                  readOnly
                  lang="markdown"
                  value={run.rendered_prompt}
                  minHeight="4rem"
                  maxHeight="24rem"
                  label="Rendered prompt"
                />
              </>
            )}
          </Details>
          <Details summary="Command line">
            {run.argv.length > 0 ? (
              <pre className="m-0 overflow-auto whitespace-pre-wrap break-words rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
                {run.argv.join(" ")}
              </pre>
            ) : (
              <div className="text-muted">(none)</div>
            )}
          </Details>
          <Details summary="Capabilities">
            {run.capabilities === null || run.capabilities === undefined ? (
              <div className="text-muted">
                Not reported (initialize did not complete).
              </div>
            ) : (
              <JsonView value={run.capabilities} />
            )}
          </Details>
        </div>
      </div>
    </div>
  );
}
