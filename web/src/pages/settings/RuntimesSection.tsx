import { Fragment, useState } from "react";
import { errorMessage } from "../../api/client";
import {
  useProbeRuntime,
  useRuntimes,
  type ProbeResult,
  type Runtime,
} from "../../api/settings";
import { JsonView } from "../../components/JsonView";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList, rowEntities } from "../../lib/contextSummary";
import { formatDuration } from "../../lib/format";

export function RuntimesSection() {
  const runtimes = useRuntimes();
  const probe = useProbeRuntime();
  const [open, setOpen] = useState<string | null>(null);
  // Names, modes and probe outcome only: command lines and env are left out.
  usePageContext(
    runtimes.data
      ? {
          title: "Settings: Runtimes",
          facts: factList([["runtimes", runtimes.data.length]]),
          entities: rowEntities(runtimes.data, (r) => ({
            kind: "runtime",
            id: r.name,
            detail: [
              r.modes.ask && `ask: ${r.modes.ask}`,
              r.modes.full && `full: ${r.modes.full}`,
              r.last_probe
                ? `last probe ${r.last_probe.ok ? "ok" : "failed"}`
                : "not probed",
            ]
              .filter(Boolean)
              .join(", "),
          })),
        }
      : null,
  );

  if (runtimes.isPending) return <LoadingState />;
  if (runtimes.isError) {
    return (
      <ErrorState
        error={runtimes.error}
        onRetry={() => void runtimes.refetch()}
      />
    );
  }
  if (runtimes.data.length === 0) {
    return (
      <EmptyState
        title="No runtimes configured"
        hint="Add a runtime under `runtimes` in config.yaml. Agent nodes pick one by name."
      />
    );
  }

  const probing = probe.isPending ? probe.variables : undefined;

  return (
    <Panel
      title="ACP runtimes"
      actions={
        <Button icon="refresh" onClick={() => void runtimes.refetch()}>
          Refresh
        </Button>
      }
    >
      <Table>
        <thead>
          <tr>
            <Th>Name</Th>
            <Th>Command</Th>
            <Th>Modes</Th>
            <Th>Env keys</Th>
            <Th>Last probe</Th>
            <Th className="w-px" />
          </tr>
        </thead>
        <tbody>
          {runtimes.data.map((r) => (
            <Fragment key={r.name}>
              <tr>
                <Td className="font-mono">{r.name}</Td>
                <Td className="font-mono">{r.command.join(" ")}</Td>
                <Td className="text-muted">
                  {[
                    r.modes.ask && `ask: ${r.modes.ask}`,
                    r.modes.full && `full: ${r.modes.full}`,
                  ]
                    .filter(Boolean)
                    .join(", ") || "–"}
                </Td>
                <Td className="font-mono text-muted">
                  {r.env_keys?.length ? r.env_keys.join(", ") : "–"}
                </Td>
                <Td>
                  <ProbeSummary
                    runtime={r}
                    open={open === r.name}
                    onToggle={() => setOpen(open === r.name ? null : r.name)}
                  />
                </Td>
                <Td>
                  <Button
                    icon="play"
                    disabled={probing !== undefined}
                    onClick={() =>
                      probe.mutate(r.name, { onSuccess: () => setOpen(r.name) })
                    }
                  >
                    {probing === r.name ? "Probing…" : "Probe"}
                  </Button>
                </Td>
              </tr>
              {probe.isError && probe.variables === r.name && (
                <tr>
                  <td
                    colSpan={6}
                    className="border-b border-line px-3 py-1.5 text-bad"
                  >
                    Could not probe {r.name}: {errorMessage(probe.error)}
                  </td>
                </tr>
              )}
              {open === r.name && r.last_probe && (
                <tr>
                  <td
                    colSpan={6}
                    className="border-b border-line bg-raised/40 px-3 py-1.5"
                  >
                    <ProbeDetail probe={r.last_probe} runtime={r} />
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </Table>
    </Panel>
  );
}

function ProbeSummary({
  runtime,
  open,
  onToggle,
}: {
  runtime: Runtime;
  open: boolean;
  onToggle: () => void;
}) {
  const p = runtime.last_probe;
  if (!p) return <span className="text-muted">not probed</span>;
  return (
    <div className="flex items-center gap-2">
      <Badge tone={p.ok ? "ok" : "bad"}>{p.ok ? "ok" : "failed"}</Badge>
      <Time ts={p.checked_at} />
      <button
        type="button"
        className="text-accent hover:underline"
        aria-expanded={open}
        onClick={onToggle}
      >
        {open ? "Hide" : "Details"}
      </button>
    </div>
  );
}

function ProbeDetail({
  probe,
  runtime,
}: {
  probe: ProbeResult;
  runtime: Runtime;
}) {
  const modes = probe.available_modes ?? [];
  const models = probe.available_models ?? [];
  return (
    <div className="space-y-3 py-1">
      {probe.error && (
        <p role="alert" className="m-0 break-words font-mono text-bad">
          {probe.error}
        </p>
      )}
      <dl className="m-0 grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted">Duration</dt>
        <dd className="m-0">{formatDuration(probe.duration_ms)}</dd>
        {probe.agent_info && (
          <>
            <dt className="text-muted">Agent</dt>
            <dd className="m-0">
              {probe.agent_info.title ?? probe.agent_info.name}
              {probe.agent_info.version ? ` ${probe.agent_info.version}` : ""}
            </dd>
          </>
        )}
        {probe.protocol_version != null && (
          <>
            <dt className="text-muted">Protocol version</dt>
            <dd className="m-0">{probe.protocol_version}</dd>
          </>
        )}
        {probe.load_session !== undefined && (
          <>
            <dt className="text-muted">Resume sessions</dt>
            <dd className="m-0">{probe.load_session ? "yes" : "no"}</dd>
          </>
        )}
        {runtime.modes.ask && (
          <>
            <dt className="text-muted">Ask mode</dt>
            <dd className="m-0">
              {runtime.modes.ask}{" "}
              <ModeBadge available={probe.ask_mode_available} />
            </dd>
          </>
        )}
        {runtime.modes.full && (
          <>
            <dt className="text-muted">Full mode</dt>
            <dd className="m-0">
              {runtime.modes.full}{" "}
              <ModeBadge available={probe.full_mode_available} />
            </dd>
          </>
        )}
        {modes.length > 0 && (
          <>
            <dt className="text-muted">Session modes</dt>
            <dd className="m-0 font-mono">
              {modes.map((m) => m.id).join(", ")}
            </dd>
          </>
        )}
        {models.length > 0 && (
          <>
            <dt className="text-muted">Models</dt>
            <dd className="m-0 font-mono">
              {models.map((m) => m.id).join(", ")}
            </dd>
          </>
        )}
      </dl>
      {probe.stderr && (
        <div>
          <div className="mb-1 text-[11px] font-medium text-muted">
            stderr (tail)
          </div>
          <pre className="m-0 max-h-48 overflow-auto rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
            {probe.stderr}
          </pre>
        </div>
      )}
      {probe.capabilities !== undefined && probe.capabilities !== null && (
        <details>
          <summary className="cursor-pointer text-[11px] font-medium text-muted">
            Capabilities
          </summary>
          <JsonView value={probe.capabilities} maxHeight="14rem" />
        </details>
      )}
    </div>
  );
}

function ModeBadge({ available }: { available: boolean | undefined }) {
  if (available === undefined) return null;
  return (
    <Badge tone={available ? "ok" : "warn"}>
      {available ? "offered by agent" : "not offered by agent"}
    </Badge>
  );
}
