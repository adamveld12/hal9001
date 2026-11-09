import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useAssistSession } from "../../api/assist";
import { ApiError, errorMessage } from "../../api/client";
import {
  useCreateFlow,
  useDeleteFlow,
  useFlows,
  type FlowRunNowResult,
} from "../../api/flows";
import { useQueue } from "../../api/queue";
import { Tabs } from "../../components/Tabs";
import { RunNowPanel } from "../../components/RunNowPanel";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Field,
  LoadingState,
  PageHeader,
  StatusBadge,
  Table,
  Td,
  TextInput,
  Th,
} from "../../components/ui";
import { emptyFlow } from "../../flow/model";
import { flowToApi } from "../../flow/apiAdapter";
import { AssistDrawer } from "../assist/AssistDrawer";
import { AssistPageContext, useAssistPage } from "../assist/context";
import { targetConfig } from "../assist/targets";
import { useBoundSession } from "../assist/useDraftSync";
import { FlowQueue } from "../queue/FlowQueue";
import { FlowRunsList } from "../runs/FlowRunsList";
import {
  filtersToParams,
  flowRunFiltersFromParams,
  type FlowRunFilters,
} from "../runs/runList";
import { FlowDraftView } from "./FlowDraftView";
import { FlowRunNow } from "./FlowRunNow";
import { useFlowAssistPage } from "./useFlowAssistPage";

const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

function NewFlow({ existing }: { existing: string[] }) {
  const [id, setId] = useState("");
  const create = useCreateFlow();
  const navigate = useNavigate();
  const problem =
    id === ""
      ? undefined
      : !SLUG.test(id)
        ? "Use letters, digits, - and _ (max 64)."
        : existing.includes(id)
          ? "A flow with this id exists."
          : undefined;
  const err = create.error;
  const diagnostics =
    err instanceof ApiError
      ? (err.problem?.diagnostics as { message: string }[] | undefined)
      : undefined;
  return (
    <form
      className="flex items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (id === "" || problem) return;
        create.mutate(flowToApi({ ...emptyFlow(id), enabled: false }), {
          onSuccess: (d) => void navigate(`/flows/${d.id}`),
        });
      }}
    >
      <Field
        label="New flow id"
        error={
          problem
            ? [problem]
            : (diagnostics?.map((d) => d.message) ??
              (err ? [errorMessage(err)] : undefined))
        }
      >
        <TextInput
          value={id}
          placeholder="triage-github"
          onChange={(e) => setId(e.target.value)}
          invalid={!!problem}
        />
      </Field>
      <Button
        type="submit"
        variant="primary"
        icon="plus"
        disabled={id === "" || !!problem || create.isPending}
      >
        Create
      </Button>
    </form>
  );
}

/** The pane beside the drawer while a new flow is drafted: the session's canvas, or a hint before it exists. */
function DraftPane() {
  const bound = useBoundSession(null);
  if (bound.sessionId !== null) {
    return (
      <div className="min-h-0 flex-1">
        <FlowDraftView sessionId={bound.sessionId} />
      </div>
    );
  }
  return (
    <EmptyState
      title="Describe the flow to the assistant"
      hint="The draft appears here as a graph. Nothing is saved until you press Save in the assistant."
    />
  );
}

function FlowsPageBody() {
  const flows = useFlows();
  const del = useDeleteFlow();
  const [runNow, setRunNow] = useState<{
    result: FlowRunNowResult;
    note?: string;
  } | null>(null);
  const assist = useAssistPage();
  const navigate = useNavigate();
  const drafting = assist.param !== null;
  const [params, setParams] = useSearchParams();
  const view: "flows" | "runs" | "queue" =
    params.get("view") === "runs"
      ? "runs"
      : params.get("view") === "queue"
        ? "queue"
        : "flows";
  const runFilters = flowRunFiltersFromParams(params);
  const queue = useQueue();
  const setView = (v: "flows" | "runs" | "queue") => {
    const next = new URLSearchParams(params);
    next.delete("view");
    for (const k of ["flow", "status", "kind"]) next.delete(k);
    if (v !== "flows") next.set("view", v);
    setParams(next, { replace: true });
  };
  const setRunFilters = (f: FlowRunFilters) =>
    setParams(filtersToParams(f, params), { replace: true });
  const showRuns = !drafting && view === "runs";
  const showQueue = !drafting && view === "queue";
  const showFlows = !drafting && view === "flows";

  // A link to a session (an agent run, an approval) names no page: go to the one for its target.
  const linked = useAssistSession(assist.sessionId);
  const targetKind = linked.data?.target.kind;
  const targetId = linked.data?.target.id ?? null;
  useEffect(() => {
    if (assist.sessionId === null || targetKind === undefined) return;
    if (targetKind === "sensor") {
      void navigate(targetConfig("sensor").draftPath(assist.sessionId), {
        replace: true,
      });
    } else if (targetId !== null) {
      void navigate(
        targetConfig("flow").editorPath(targetId, assist.sessionId),
        { replace: true },
      );
    }
  }, [assist.sessionId, targetKind, targetId, navigate]);

  return (
    <div className="flex h-full">
      <div className="flex h-full min-w-0 flex-1 flex-col">
        <PageHeader
          title="Flows"
          subtitle="Each flow is a YAML file under flows/"
          actions={
            <>
              {flows.data && <NewFlow existing={flows.data.map((f) => f.id)} />}
              <Button
                icon="agent"
                disabled={drafting}
                title="Chat with an AI agent that drafts a new flow"
                onClick={assist.open}
              >
                Create with AI
              </Button>
            </>
          }
        />
        {runNow && (
          <RunNowPanel
            result={runNow.result}
            note={runNow.note}
            onClose={() => setRunNow(null)}
          />
        )}
        {!drafting && (
          <Tabs
            tabs={[
              { key: "flows", label: "Flows" },
              { key: "runs", label: "Runs" },
              { key: "queue", label: "Queue", count: queue.data?.total },
            ]}
            active={view}
            onChange={setView}
          />
        )}
        {drafting && <DraftPane />}
        {showRuns && (
          <FlowRunsList filters={runFilters} onChange={setRunFilters} />
        )}
        {showQueue && (
          <FlowQueue
            flow={params.get("flow") ?? ""}
            onFlowChange={(flow) => {
              const next = new URLSearchParams(params);
              if (flow) next.set("flow", flow);
              else next.delete("flow");
              setParams(next, { replace: true });
            }}
          />
        )}
        {showFlows && flows.isPending && <LoadingState />}
        {showFlows && flows.isError && (
          <ErrorState
            error={flows.error}
            onRetry={() => void flows.refetch()}
          />
        )}
        {showFlows && flows.data?.length === 0 && (
          <EmptyState
            title="No flows yet"
            hint="Create one above, then drag sensors and nodes onto the canvas."
          />
        )}
        {showFlows && flows.data && flows.data.length > 0 && (
          <div className="overflow-auto">
            <Table>
              <thead>
                <tr>
                  <Th>Flow</Th>
                  <Th>State</Th>
                  <Th>Sensors</Th>
                  <Th>Nodes</Th>
                  <Th>Last run</Th>
                  <Th>7d runs</Th>
                  <Th>Error rate</Th>
                  <Th>Problems</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {flows.data.map((f) => {
                  const errors = f.diagnostics.filter(
                    (d) => d.severity === "error",
                  ).length;
                  const warnings = f.diagnostics.filter(
                    (d) => d.severity === "warning",
                  ).length;
                  const h = f.health;
                  return (
                    <tr key={f.id} className="hover:bg-raised/60">
                      <Td>
                        <Link
                          to={`/flows/${f.id}`}
                          className="font-medium text-accent"
                        >
                          {f.id}
                        </Link>
                        {f.description ? (
                          <div className="text-xs text-muted">
                            {f.description}
                          </div>
                        ) : null}
                      </Td>
                      <Td>
                        <div className="flex gap-1">
                          {f.enabled === null ? (
                            <Badge tone="bad">unparseable</Badge>
                          ) : f.enabled ? (
                            <Badge tone="ok">enabled</Badge>
                          ) : (
                            <Badge>disabled</Badge>
                          )}
                          {f.dry_run && <Badge tone="info">dry run</Badge>}
                        </div>
                      </Td>
                      <Td className="text-muted">
                        {f.subscribe.join(", ") || "–"}
                      </Td>
                      <Td className="tabular-nums">{f.node_count}</Td>
                      <Td>
                        {h.last_run_id ? (
                          <Link
                            to={`/runs/${h.last_run_id}`}
                            className="inline-flex items-center gap-1.5"
                          >
                            {h.last_run_status && (
                              <StatusBadge status={h.last_run_status} />
                            )}
                            <Time ts={h.last_run_at} />
                          </Link>
                        ) : (
                          <span className="text-muted">never</span>
                        )}
                      </Td>
                      <Td className="tabular-nums">
                        {h.runs}
                        {h.running + h.queued > 0 && (
                          <span className="text-muted">
                            {" "}
                            ({h.running} running, {h.queued} queued)
                          </span>
                        )}
                        {h.awaiting_approval > 0 && (
                          <Badge tone="warn" className="ml-1">
                            {h.awaiting_approval} awaiting
                          </Badge>
                        )}
                      </Td>
                      <Td className="tabular-nums">
                        {h.runs === 0
                          ? "–"
                          : `${Math.round(h.error_rate * 100)}%`}
                      </Td>
                      <Td>
                        {errors > 0 && (
                          <Badge tone="bad">
                            {errors} error{errors === 1 ? "" : "s"}
                          </Badge>
                        )}{" "}
                        {warnings > 0 && (
                          <Badge tone="warn">
                            {warnings} warning{warnings === 1 ? "" : "s"}
                          </Badge>
                        )}
                        {errors + warnings === 0 && (
                          <span className="text-muted">–</span>
                        )}
                      </Td>
                      <Td className="text-right">
                        <span className="inline-flex items-start gap-1">
                          <FlowRunNow
                            flowId={f.id}
                            enabled={f.enabled === true}
                            subscribeCount={
                              f.enabled === null ? 0 : f.subscribe.length
                            }
                            onResult={(result, note) =>
                              setRunNow({ result, note })
                            }
                          />
                          <Button
                            variant="ghost"
                            icon="trash"
                            aria-label={`Delete ${f.id}`}
                            disabled={del.isPending}
                            onClick={() => {
                              if (
                                window.confirm(
                                  `Delete flows/${f.id}.yaml? Past runs keep their snapshots.`,
                                )
                              ) {
                                del.mutate({ id: f.id, expectedHash: f.hash });
                              }
                            }}
                          />
                        </span>
                      </Td>
                    </tr>
                  );
                })}
              </tbody>
            </Table>
            {del.isError && <ErrorState error={del.error} />}
          </div>
        )}
      </div>
      {drafting && <AssistDrawer />}
    </div>
  );
}

export function FlowsPage() {
  const assist = useFlowAssistPage(null);
  return (
    <AssistPageContext.Provider value={assist}>
      <FlowsPageBody />
    </AssistPageContext.Provider>
  );
}
