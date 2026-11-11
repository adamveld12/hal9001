import { useState, type ReactNode } from "react";
import { errorMessage } from "../../api/client";
import {
  useAdapterProgress,
  useDetectRuntimes,
  useInstallAdapter,
  useProbeRuntime,
  useRemoveAdapter,
  useRuntimes,
  type ProbeResult,
  type Runtime,
} from "../../api/settings";
import { CodeBlock } from "../../components/CodeBlock";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { ExternalLink } from "../../components/ExternalLink";
import { JsonView } from "../../components/JsonView";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Spinner,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList, rowEntities } from "../../lib/contextSummary";
import { formatDuration } from "../../lib/format";
import {
  adapterAction,
  type AdapterAction,
  adapterSummary,
  authSummary,
  nodeSummary,
  orderWarnings,
  statusLabel,
  statusTone,
  type ProgressState,
} from "../../lib/runtimeStatus";
import { TONE_TEXT, type Tone } from "../../lib/status";

type Pending = { kind: "install" | "remove"; runtime: Runtime };

export function RuntimesSection() {
  const runtimes = useRuntimes();
  const probe = useProbeRuntime();
  const detect = useDetectRuntimes();
  const install = useInstallAdapter();
  const remove = useRemoveAdapter();
  const progress = useAdapterProgress();
  const [open, setOpen] = useState<string | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  // Names, modes, detection status and probe outcome only: command lines and env are left out.
  usePageContext(
    runtimes.data
      ? {
          title: "Settings: Runtimes",
          facts: factList([["runtimes", runtimes.data.length]]),
          entities: rowEntities(runtimes.data, (r) => ({
            kind: "runtime",
            id: r.name,
            detail: [
              statusLabel(r.detection.status).toLowerCase(),
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
  const checkedAt = runtimes.data
    .map((r) => r.detection.checked_at)
    .reduce((a, b) => (a > b ? a : b));

  const confirm = () => {
    if (!pending) return;
    const { kind, runtime } = pending;
    const m = kind === "install" ? install : remove;
    m.mutate(runtime.name, { onSettled: () => setPending(null) });
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <h2 className="m-0 text-sm font-semibold">ACP runtimes</h2>
        <span className="text-muted">
          Checked <Time ts={checkedAt} />
        </span>
        <Button
          className="ml-auto"
          icon="refresh"
          disabled={detect.isPending}
          onClick={() => detect.mutate()}
        >
          {detect.isPending ? "Checking…" : "Re-check"}
        </Button>
      </div>
      {detect.isError && (
        <p role="alert" className="m-0 text-xs text-bad">
          Could not re-check: {errorMessage(detect.error)}
        </p>
      )}
      {runtimes.data.map((r) => (
        <RuntimeCard
          key={r.name}
          runtime={r}
          progress={progress[r.name]}
          probing={probing === r.name}
          probeDisabled={probing !== undefined}
          probeError={
            probe.isError && probe.variables === r.name
              ? errorMessage(probe.error)
              : undefined
          }
          adapterError={
            (install.isError && install.variables === r.name
              ? errorMessage(install.error)
              : undefined) ??
            (remove.isError && remove.variables === r.name
              ? errorMessage(remove.error)
              : undefined)
          }
          adapterBusy={
            (install.isPending && install.variables === r.name) ||
            (remove.isPending && remove.variables === r.name)
          }
          probeOpen={open === r.name}
          onToggleProbe={() => setOpen(open === r.name ? null : r.name)}
          onProbe={() =>
            probe.mutate(r.name, { onSuccess: () => setOpen(r.name) })
          }
          onInstall={() => setPending({ kind: "install", runtime: r })}
          onRemove={() => setPending({ kind: "remove", runtime: r })}
        />
      ))}
      <ConfirmDialog
        open={pending !== null}
        title={
          pending?.kind === "remove"
            ? `Remove the ${pending.runtime.name} adapter?`
            : `Install the ${pending?.runtime.name ?? ""} adapter?`
        }
        confirmLabel={pending?.kind === "remove" ? "Remove" : "Install"}
        confirmVariant={pending?.kind === "remove" ? "danger" : "primary"}
        busy={install.isPending || remove.isPending}
        onCancel={() => setPending(null)}
        onConfirm={confirm}
      >
        {pending && <ConfirmBody pending={pending} />}
      </ConfirmDialog>
    </div>
  );
}

function ConfirmBody({ pending }: { pending: Pending }) {
  const { adapter } = pending.runtime.detection;
  if (pending.kind === "remove") {
    return (
      <p className="m-0">
        Deletes the adapter hal installed
        {adapter.path ? (
          <>
            {" "}
            from <code className="font-mono">{adapter.path}</code>
          </>
        ) : null}
        . The {pending.runtime.name} CLI itself is not touched. You can install
        the adapter again later.
      </p>
    );
  }
  return (
    <div className="space-y-2">
      <p className="m-0">
        hal will run <code className="font-mono">npm install</code> to fetch the
        ACP adapter for {pending.runtime.name}
        {adapter.pinned_version ? ` (v${adapter.pinned_version})` : ""} from the
        npm registry, so it needs network access.
      </p>
      <p className="m-0">
        It is installed into hal&rsquo;s own data folder, not globally, and
        needs Node 22 or newer. Nothing is installed outside that folder, and
        you can remove it here at any time.
      </p>
    </div>
  );
}

function RuntimeCard({
  runtime: r,
  progress,
  probing,
  probeDisabled,
  probeError,
  adapterError,
  adapterBusy,
  probeOpen,
  onToggleProbe,
  onProbe,
  onInstall,
  onRemove,
}: {
  runtime: Runtime;
  progress: ProgressState | undefined;
  probing: boolean;
  probeDisabled: boolean;
  probeError: string | undefined;
  adapterError: string | undefined;
  adapterBusy: boolean;
  probeOpen: boolean;
  onToggleProbe: () => void;
  onProbe: () => void;
  onInstall: () => void;
  onRemove: () => void;
}) {
  const d = r.detection;
  const auth = authSummary(d.auth);
  const node = nodeSummary(d.node);
  const action = adapterAction(d, progress);
  const warnings = orderWarnings(d.warnings).filter(
    (w) => !(w.code === "adapter_missing" && action.kind === "installing"),
  );
  const command = d.command ?? r.command;
  const modes = [
    r.modes.ask && `ask: ${r.modes.ask}`,
    r.modes.full && `full: ${r.modes.full}`,
  ].filter(Boolean);
  return (
    <section
      aria-label={`Runtime ${r.name}`}
      className="rounded-md border border-line bg-panel"
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2 text-xs">
        <span className="font-mono text-sm font-semibold">{r.name}</span>
        {r.kind && r.kind !== r.name && <Badge>{r.kind}</Badge>}
        <Badge tone={statusTone(d.status)}>{statusLabel(d.status)}</Badge>
        <Button
          className="ml-auto"
          icon="play"
          disabled={probeDisabled}
          onClick={onProbe}
        >
          {probing ? "Probing…" : "Probe"}
        </Button>
      </header>
      <div className="space-y-3 p-3 text-xs">
        <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-1">
          <Row label="CLI">
            {d.cli.found ? (
              <>
                <span className="font-mono">{d.cli.path ?? d.cli.name}</span>
                {d.cli.version && (
                  <span className="text-muted"> · {d.cli.version}</span>
                )}
              </>
            ) : (
              <Status tone="bad">not found</Status>
            )}
          </Row>
          <Row label="Sign-in">
            <Status tone={auth.tone}>{auth.text}</Status>
          </Row>
          <Row label="Adapter">{adapterSummary(d.adapter)}</Row>
          {(d.adapter.source === "managed" || d.adapter.installable) && (
            <Row label="Node">
              <Status tone={node.tone}>{node.text}</Status>
            </Row>
          )}
          {modes.length > 0 && (
            <Row label="Modes">
              <span className="text-muted">{modes.join(", ")}</span>
            </Row>
          )}
          {r.env_keys && r.env_keys.length > 0 && (
            <Row label="Env keys">
              <span className="font-mono text-muted">
                {r.env_keys.join(", ")}
              </span>
            </Row>
          )}
        </dl>

        {warnings.length > 0 && (
          <ul className="list-none space-y-2 p-0">
            {warnings.map((w) => (
              <li
                key={w.code}
                className="space-y-1.5 rounded-md border border-warn/40 bg-warn/5 p-2"
              >
                <div className="text-warn">{w.message}</div>
                {w.command && <CodeBlock text={w.command} label="Copy" />}
                {w.url && (
                  <ExternalLink
                    href={w.url}
                    className="inline-block text-accent hover:underline"
                  >
                    {w.url}
                  </ExternalLink>
                )}
              </li>
            ))}
          </ul>
        )}

        <AdapterControls
          action={action}
          progress={progress}
          busy={adapterBusy}
          error={adapterError}
          onInstall={onInstall}
          onRemove={onRemove}
        />

        <details>
          <summary className="cursor-pointer text-[11px] font-medium text-muted">
            Details
          </summary>
          <div className="mt-1.5 space-y-1.5">
            <div className="text-muted">Command hal runs</div>
            {command.length > 0 ? (
              <CodeBlock text={command.join(" ")} />
            ) : (
              <span className="text-muted">none resolved</span>
            )}
            {d.adapter.path && (
              <div className="text-muted">
                Adapter at <span className="font-mono">{d.adapter.path}</span>
              </div>
            )}
          </div>
        </details>

        <div className="flex flex-wrap items-center gap-2 border-t border-line pt-2">
          <span className="text-muted">Last probe</span>
          <ProbeSummary runtime={r} open={probeOpen} onToggle={onToggleProbe} />
        </div>
        {probeError && (
          <p role="alert" className="m-0 text-bad">
            Could not probe {r.name}: {probeError}
          </p>
        )}
        {probeOpen && r.last_probe && (
          <div className="rounded-md bg-raised/40 px-3 py-1.5">
            <ProbeDetail probe={r.last_probe} runtime={r} />
          </div>
        )}
      </div>
    </section>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted">{label}</dt>
      <dd className="m-0 min-w-0 break-all">{children}</dd>
    </>
  );
}

function Status({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={TONE_TEXT[tone]}>{children}</span>;
}

function AdapterControls({
  action,
  progress,
  busy,
  error,
  onInstall,
  onRemove,
}: {
  action: AdapterAction;
  progress: ProgressState | undefined;
  busy: boolean;
  error: string | undefined;
  onInstall: () => void;
  onRemove: () => void;
}) {
  const failed = progress?.phase === "failed";
  const done = progress?.phase === "done";
  return (
    <div className="space-y-1.5">
      {action.kind === "install" && (
        <Button variant="primary" disabled={busy} onClick={onInstall}>
          {busy ? "Starting…" : action.label}
        </Button>
      )}
      {action.kind === "install_disabled" && (
        <div className="space-y-1">
          <Button variant="primary" disabled>
            Install adapter
          </Button>
          <p className="m-0 text-muted">{action.reason}</p>
        </div>
      )}
      {action.kind === "installing" && (
        <div className="space-y-1.5">
          <Spinner label="Installing adapter" />
          {action.lines.length > 0 && (
            <pre
              aria-label="Install output"
              className="m-0 max-h-28 overflow-auto rounded-md border border-line bg-raised p-2 font-mono text-[11px]"
            >
              {action.lines.join("\n")}
            </pre>
          )}
        </div>
      )}
      {action.kind === "remove" && (
        <Button
          variant="danger"
          icon="trash"
          disabled={busy}
          onClick={onRemove}
        >
          {busy ? "Removing…" : "Remove adapter"}
        </Button>
      )}
      {done && action.kind !== "installing" && (
        <p role="status" className="m-0 text-ok">
          Adapter installed.
        </p>
      )}
      {failed && action.kind !== "installing" && (
        <div role="alert" className="space-y-1 text-bad">
          <p className="m-0">
            Install failed{progress.error ? `: ${progress.error}` : "."}
          </p>
          {progress.lines.length > 0 && (
            <pre className="m-0 max-h-28 overflow-auto rounded-md border border-line bg-raised p-2 font-mono text-[11px] text-fg">
              {progress.lines.join("\n")}
            </pre>
          )}
        </div>
      )}
      {error && (
        <p role="alert" className="m-0 text-bad">
          {error}
        </p>
      )}
    </div>
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
