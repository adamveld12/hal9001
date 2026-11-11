import type { ReactNode } from "react";
import type { TailscaleStatus, TailscaleWarning } from "../../api/tailscale";
import { CodeBlock } from "../../components/CodeBlock";
import { CopyButton } from "../../components/CopyButton";
import { ExternalLink } from "../../components/ExternalLink";
import { Icon } from "../../components/icons";
import { Badge } from "../../components/ui";
import { TONE_TEXT } from "../../lib/status";
import {
  allowedHostEntry,
  allowedHostView,
  funnelMessage,
  funnelOffCommand,
  serveCommand,
  shareUrl,
  stopCommand,
  warningTone,
  type ServeFailure,
} from "./tailscaleView";

export const LINK = "text-accent underline";
const LINK_BUTTON =
  "inline-flex h-7 items-center gap-1.5 rounded-md border border-line bg-panel px-2.5 text-xs font-medium no-underline hover:bg-raised";

export function Row({
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
export function SharedBlock({ status }: { status: TailscaleStatus }) {
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

export function EnableBody({
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

export function DisableBody({ status }: { status: TailscaleStatus }) {
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
export function FunnelBlock({ port }: { port: number }) {
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

export function FailureBlock({ failure }: { failure: ServeFailure }) {
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

export function AllowedHostRow({ status }: { status: TailscaleStatus }) {
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
