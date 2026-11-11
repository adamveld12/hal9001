import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import {
  useDisableTailscaleServe,
  useEnableTailscaleServe,
  useRefreshTailscale,
  useTailscaleStatus,
  type TailscaleStatus,
  type TailscaleWarning,
} from "../../api/tailscale";
import { CodeBlock } from "../../components/CodeBlock";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { CopyButton } from "../../components/CopyButton";
import { ExternalLink } from "../../components/ExternalLink";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  TextInput,
} from "../../components/ui";
import { Icon } from "../../components/icons";
import { usePageContext } from "../../layout/pageContext";
import { factList } from "../../lib/contextSummary";
import { TONE_TEXT } from "../../lib/status";
import {
  allowedHostEntry,
  allowedHostView,
  conflictTitle,
  funnelMessage,
  funnelOffCommand,
  loginsView,
  parsePort,
  parseServeFailure,
  serveCommand,
  shareControl,
  shareUrl,
  statusRows,
  stopCommand,
  warningTone,
  type ServeFailure,
} from "./tailscaleView";

const LINK = "text-accent underline";
const LINK_BUTTON =
  "inline-flex h-7 items-center gap-1.5 rounded-md border border-line bg-panel px-2.5 text-xs font-medium no-underline hover:bg-raised";

export function TailscaleSection() {
  const ts = useTailscaleStatus();
  // Booleans and states only: no machine name, tailnet or login.
  usePageContext(
    ts.data
      ? {
          title: "Settings: Tailscale",
          facts: factList([
            ["tailscale cli found", ts.data.cli.found],
            ["signed in", ts.data.logged_in],
            ["tailscale state", ts.data.backend_state],
            ["https certificates enabled", ts.data.https_enabled],
            ["shared on tailnet", ts.data.serve.points_at_hal],
            ["https port", ts.data.serve.https_port],
            ["host in allowed_hosts", ts.data.allowed_host.present],
            ["warnings", ts.data.warnings.map((w) => w.code).join(", ")],
          ]),
        }
      : null,
  );
  if (ts.isPending) return <LoadingState />;
  if (ts.isError) {
    return <ErrorState error={ts.error} onRetry={() => void ts.refetch()} />;
  }
  return <TailscaleDetails status={ts.data} />;
}

type Dialog = "enable" | "disable" | null;
type Conflict = Extract<ServeFailure, { kind: "conflict" }> & { port: number };

function TailscaleDetails({ status }: { status: TailscaleStatus }) {
  const refresh = useRefreshTailscale();
  const enable = useEnableTailscaleServe();
  const disable = useDisableTailscaleServe();
  const [portText, setPortText] = useState<string | null>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [conflict, setConflict] = useState<Conflict | null>(null);
  const [failure, setFailure] = useState<ServeFailure | null>(null);

  const portField = portText ?? String(status.serve.https_port);
  const parsed = parsePort(portField);
  const port = parsed.ok ? parsed.port : status.serve.https_port;
  const control = shareControl(status, port);
  const busy = enable.isPending || disable.isPending;

  const startEnable = (replace: boolean) => {
    if (!parsed.ok) return;
    setFailure(null);
    enable.mutate(
      replace
        ? { https_port: parsed.port, replace: true }
        : { https_port: parsed.port },
      {
        onSuccess: () => {
          setDialog(null);
          setConflict(null);
        },
        onError: (e) => {
          const f = parseServeFailure(e);
          setDialog(null);
          if (f.kind === "conflict" && !replace) {
            setConflict({ ...f, port: parsed.port });
          } else {
            setConflict(null);
            setFailure(f);
          }
        },
      },
    );
  };

  const startDisable = () => {
    setFailure(null);
    disable.mutate(undefined, {
      onSuccess: () => setDialog(null),
      onError: (e) => {
        setDialog(null);
        setFailure(parseServeFailure(e));
      },
    });
  };

  return (
    <div className="space-y-4" data-testid="tailscale-section">
      <Panel
        title="Tailscale"
        actions={
          <>
            <span className="text-muted" data-testid="tailscale-checked-at">
              Checked <Time ts={status.checked_at} />
            </span>
            <Button
              icon="refresh"
              data-testid="tailscale-refresh"
              disabled={refresh.isPending}
              onClick={() => refresh.mutate()}
            >
              {refresh.isPending ? "Checking…" : "Check again"}
            </Button>
          </>
        }
      >
        <dl className="m-0 grid grid-cols-[11rem_1fr] gap-x-3 gap-y-2 text-xs">
          {statusRows(status).map((row) => (
            <Row
              key={row.key}
              label={row.label}
              testId={`tailscale-row-${row.key}`}
            >
              <span className={TONE_TEXT[row.tone]}>{row.text}</span>
              {row.detail && <div className="text-muted">{row.detail}</div>}
              {row.link && (
                <ExternalLink href={row.link.href} className={LINK}>
                  {row.link.label}
                </ExternalLink>
              )}
            </Row>
          ))}
        </dl>
        {refresh.isError && (
          <p role="alert" className="m-0 mt-2 text-xs text-bad">
            Could not check again: {refresh.error.message}
          </p>
        )}
      </Panel>

      <Panel title="Share hal on your tailnet">
        <div className="space-y-3 text-xs">
          {control.shared ? (
            <SharedBlock status={status} />
          ) : (
            <p className="m-0 text-muted">
              Make hal reachable from your other devices over HTTPS, on your
              tailnet only. It is not exposed to the public internet.
            </p>
          )}

          {!control.shared && (
            <div className="max-w-xs">
              <Field
                label="HTTPS port"
                hint="The tailnet port hal answers on. 443 gives a URL without a port."
                error={parsed.ok ? undefined : [parsed.error]}
              >
                <TextInput
                  inputMode="numeric"
                  data-testid="tailscale-port-input"
                  value={portField}
                  invalid={!parsed.ok}
                  disabled={busy}
                  onChange={(e) => setPortText(e.target.value)}
                />
              </Field>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {!control.shared && (
              <Button
                variant="primary"
                icon="external"
                data-testid="tailscale-share-button"
                disabled={
                  control.shareDisabledReason !== null || !parsed.ok || busy
                }
                onClick={() => setDialog("enable")}
              >
                Share hal on your tailnet
              </Button>
            )}
            {control.canStop && (
              <Button
                variant="danger"
                data-testid="tailscale-stop-button"
                disabled={busy}
                onClick={() => setDialog("disable")}
              >
                Stop sharing
              </Button>
            )}
          </div>
          {status.serve.funnel_on_port && (
            <FunnelBlock port={status.serve.https_port} />
          )}
          {!control.shared &&
            control.shareDisabledReason &&
            !status.serve.funnel_on_port && (
              <p
                className="m-0 text-warn"
                data-testid="tailscale-share-disabled-reason"
              >
                {control.shareDisabledReason}
              </p>
            )}
          {status.serve.enabled && !status.serve.points_at_hal && (
            <p className="m-0 text-warn" data-testid="tailscale-serve-missing">
              hal is set to keep sharing, but the mapping on port{" "}
              {status.serve.https_port} is not in place. Share again to restore
              it, or stop sharing to turn it off.
            </p>
          )}
          {failure && <FailureBlock failure={failure} />}
        </div>
      </Panel>

      <Panel title="Access">
        <dl className="m-0 grid grid-cols-[11rem_1fr] gap-x-3 gap-y-2 text-xs">
          <AllowedHostRow status={status} />
          <LoginsRow status={status} />
        </dl>
      </Panel>

      {status.warnings.length > 0 && (
        <Panel title="Warnings">
          <div data-testid="tailscale-warnings">
            <WarningList warnings={status.warnings} />
          </div>
        </Panel>
      )}

      <ConfirmDialog
        open={dialog === "enable"}
        testId="tailscale-confirm-dialog"
        title="Share hal on your tailnet?"
        confirmLabel="Share"
        confirmVariant="primary"
        busy={enable.isPending}
        confirmDisabled={!parsed.ok}
        onCancel={() => setDialog(null)}
        onConfirm={() => startEnable(false)}
      >
        <EnableBody status={status} port={port} />
      </ConfirmDialog>

      <ConfirmDialog
        open={dialog === "disable"}
        testId="tailscale-stop-dialog"
        title="Stop sharing hal?"
        confirmLabel="Stop sharing"
        busy={disable.isPending}
        onCancel={() => setDialog(null)}
        onConfirm={startDisable}
      >
        <DisableBody status={status} />
      </ConfirmDialog>

      <ConfirmDialog
        open={conflict !== null}
        testId="tailscale-replace-dialog"
        title={
          conflict ? conflictTitle(conflict, conflict.port) : "Port in use"
        }
        confirmLabel="Replace it"
        busy={enable.isPending}
        onCancel={() => setConflict(null)}
        onConfirm={() => startEnable(true)}
      >
        {conflict && (
          <div className="space-y-2">
            <p className="m-0">
              Replacing overwrites that mapping with hal. Whatever used it on
              your tailnet stops being served on port {conflict.port}.
            </p>
            {conflict.target && (
              <p className="m-0">
                Current target:{" "}
                <code
                  className="font-mono"
                  data-testid="tailscale-conflict-target"
                >
                  {conflict.target}
                </code>
              </p>
            )}
          </div>
        )}
      </ConfirmDialog>
    </div>
  );
}

function Row({
  label,
  testId,
  children,
}: {
  label: string;
  testId: string;
  children: ReactNode;
}) {
  return (
    <>
      <dt className="text-muted">{label}</dt>
      <dd className="m-0 min-w-0 break-words" data-testid={testId}>
        {children}
      </dd>
    </>
  );
}

/** The URL hal answers on, with Copy and Open. */
function SharedBlock({ status }: { status: TailscaleStatus }) {
  const url =
    status.url ??
    (status.dns_name
      ? shareUrl(status.dns_name, status.serve.https_port)
      : null);
  return (
    <div className="space-y-2" data-testid="tailscale-shared">
      <div className="flex items-center gap-2">
        <Badge tone="ok">Shared on your tailnet</Badge>
        <span className="text-muted">
          on HTTPS port {status.serve.https_port}
        </span>
      </div>
      {url ? (
        <div className="flex flex-wrap items-center gap-2">
          <code
            className="min-w-0 break-all rounded-md border border-line bg-raised px-2 py-1 font-mono"
            data-testid="tailscale-url"
          >
            {url}
          </code>
          <span data-testid="tailscale-url-copy">
            <CopyButton text={url} />
          </span>
          <ExternalLink
            href={url}
            className={LINK_BUTTON}
            data-testid="tailscale-url-open"
          >
            <Icon name="external" size={14} />
            Open
          </ExternalLink>
        </div>
      ) : (
        <p className="m-0 text-muted">The machine name is not known yet.</p>
      )}
    </div>
  );
}

function EnableBody({
  status,
  port,
}: {
  status: TailscaleStatus;
  port: number;
}) {
  const entry = status.dns_name
    ? allowedHostEntry(status.dns_name, port)
    : null;
  return (
    <div className="space-y-2">
      <p className="m-0">hal will run:</p>
      <pre
        className="m-0 overflow-auto whitespace-pre-wrap break-all rounded-md border border-line bg-raised p-2 font-mono text-xs text-fg"
        data-testid="tailscale-confirm-command"
      >
        {serveCommand(port, status.serve.target)}
      </pre>
      <ul className="m-0 list-disc space-y-1 pl-4">
        <li>
          Tailnet only: only devices signed in to your tailnet can reach hal. It
          is <b>not</b> the public internet; hal never uses{" "}
          <code className="font-mono">tailscale funnel</code>.
        </li>
        <li>
          Adds{" "}
          {entry ? (
            <code className="font-mono">{entry}</code>
          ) : (
            "the machine name"
          )}{" "}
          to <code className="font-mono">allowed_hosts</code> in config.yaml
          (unless it is already listed).
        </li>
        <li>
          It stays in place: hal sets{" "}
          <code className="font-mono">tailscale.serve</code> in config.yaml and
          applies the mapping again whenever hal restarts.
        </li>
      </ul>
    </div>
  );
}

function DisableBody({ status }: { status: TailscaleStatus }) {
  const { entry, added_by_hal } = status.allowed_host;
  return (
    <div className="space-y-2">
      <p className="m-0">hal will run:</p>
      <pre
        className="m-0 overflow-auto whitespace-pre-wrap break-all rounded-md border border-line bg-raised p-2 font-mono text-xs text-fg"
        data-testid="tailscale-stop-command"
      >
        {stopCommand(status.serve.https_port)}
      </pre>
      <ul className="m-0 list-disc space-y-1 pl-4">
        <li>
          It only removes hal&rsquo;s own mapping. A mapping on that port to
          something else is left alone.
        </li>
        <li>
          hal stops applying the mapping when it restarts (
          <code className="font-mono">tailscale.serve</code> is set to false).
        </li>
        <li>
          {added_by_hal && entry ? (
            <>
              <code className="font-mono">{entry}</code> is removed from{" "}
              <code className="font-mono">allowed_hosts</code> because hal added
              it.
            </>
          ) : (
            <>
              <code className="font-mono">allowed_hosts</code> is not changed:
              hal did not add the entry, so it stays.
            </>
          )}
        </li>
      </ul>
    </div>
  );
}

/** Funnel exposes the HTTPS port to the public internet: an error, with the command that fixes it. */
function FunnelBlock({ port }: { port: number }) {
  return (
    <div
      role="alert"
      className="space-y-2 rounded-md border border-bad/40 bg-bad/5 p-3"
      data-testid="tailscale-funnel-error"
    >
      <div className="font-medium text-bad">
        Funnel is on for HTTPS port {port}
      </div>
      <p className="m-0" data-testid="tailscale-share-disabled-reason">
        {funnelMessage(port)}
      </p>
      <CodeBlock text={funnelOffCommand(port)} />
    </div>
  );
}

function FailureBlock({ failure }: { failure: ServeFailure }) {
  if (failure.kind === "conflict") return null;
  if (failure.kind === "funnel") {
    return (
      <div
        role="alert"
        className="space-y-2 rounded-md border border-bad/40 bg-bad/5 p-3"
        data-testid="tailscale-error"
      >
        <div className="font-medium text-bad">
          Funnel is on for this HTTPS port
        </div>
        <p className="m-0">{failure.message}</p>
        {failure.httpsPort !== null && (
          <CodeBlock text={funnelOffCommand(failure.httpsPort)} />
        )}
      </div>
    );
  }
  return (
    <div
      role="alert"
      className="space-y-2 rounded-md border border-bad/40 bg-bad/5 p-3"
      data-testid="tailscale-error"
    >
      <div className="font-medium text-bad">
        {failure.kind === "not-ready"
          ? "Tailscale is not ready"
          : "The tailscale command failed"}
      </div>
      <pre className="m-0 whitespace-pre-wrap break-words font-mono text-muted">
        {failure.message}
      </pre>
      {failure.kind === "not-ready" && failure.warnings.length > 0 && (
        <WarningList warnings={failure.warnings} />
      )}
    </div>
  );
}

function AllowedHostRow({ status }: { status: TailscaleStatus }) {
  const v = allowedHostView(status);
  return (
    <Row label="allowed_hosts" testId="tailscale-allowed-host">
      <div className="flex flex-wrap items-center gap-2">
        {v.entry && <code className="font-mono">{v.entry}</code>}
        <span className={TONE_TEXT[v.tone]}>{v.text}</span>
        {status.allowed_host.added_by_hal && (
          <Badge tone="info">added by hal</Badge>
        )}
      </div>
      <div className="text-muted">{v.note}</div>
    </Row>
  );
}

function LoginsRow({ status }: { status: TailscaleStatus }) {
  const v = loginsView(status);
  return (
    <Row label="Who can sign in" testId="tailscale-logins">
      {v.logins.length > 0 ? (
        <div className="font-mono">{v.logins.join(", ")}</div>
      ) : (
        <div className="text-warn">–</div>
      )}
      <div className="text-muted">{v.note}</div>
      <Link to="/settings/auth" className={LINK}>
        Auth settings
      </Link>
    </Row>
  );
}

/** Each warning's message, a copyable fix command and a link, when it has them. */
export function WarningList({ warnings }: { warnings: TailscaleWarning[] }) {
  return (
    <ul className="m-0 list-none space-y-2 p-0">
      {warnings.map((w) => (
        <li
          key={w.code}
          className="space-y-1.5 rounded-md border border-line bg-panel p-2 text-xs"
          data-testid={`tailscale-warning-${w.code}`}
        >
          <div className="flex items-start gap-2">
            <Icon
              name="warning"
              size={14}
              className={`mt-0.5 shrink-0 ${TONE_TEXT[warningTone(w.code)]}`}
            />
            <span>{w.message}</span>
          </div>
          {w.command && (
            <div data-testid={`tailscale-warning-command-${w.code}`}>
              <CodeBlock text={w.command} />
            </div>
          )}
          {w.url && (
            <ExternalLink
              href={w.url}
              className={LINK}
              data-testid={`tailscale-warning-link-${w.code}`}
            >
              {w.url}
            </ExternalLink>
          )}
        </li>
      ))}
    </ul>
  );
}
