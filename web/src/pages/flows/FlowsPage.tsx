import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { ApiError, errorMessage } from "../../api/client";
import {
  useCreateFlow,
  useDeleteFlow,
  useFlows,
  type FlowRunNowResult,
} from "../../api/flows";
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
import { FlowRunNow } from "./FlowRunNow";

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

export function FlowsPage() {
  const flows = useFlows();
  const del = useDeleteFlow();
  const [runNow, setRunNow] = useState<{
    result: FlowRunNowResult;
    note?: string;
  } | null>(null);

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title="Flows"
        subtitle="Each flow is a YAML file under flows/"
        actions={
          flows.data && <NewFlow existing={flows.data.map((f) => f.id)} />
        }
      />
      {runNow && (
        <RunNowPanel
          result={runNow.result}
          note={runNow.note}
          onClose={() => setRunNow(null)}
        />
      )}
      {flows.isPending && <LoadingState />}
      {flows.isError && (
        <ErrorState error={flows.error} onRetry={() => void flows.refetch()} />
      )}
      {flows.data?.length === 0 && (
        <EmptyState
          title="No flows yet"
          hint="Create one above, then drag sensors and nodes onto the canvas."
        />
      )}
      {flows.data && flows.data.length > 0 && (
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
  );
}
