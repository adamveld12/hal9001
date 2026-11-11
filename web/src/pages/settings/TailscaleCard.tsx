import { useState, type ReactNode } from "react";
import { useAuthOverview } from "../../api/auth";
import {
  useDisableTailscaleServe,
  useEnableTailscaleServe,
  useRefreshTailscale,
  useTailscaleStatus,
  type TailscaleStatus,
} from "../../api/tailscale";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { ExternalLink } from "../../components/ExternalLink";
import { Time } from "../../components/Time";
import {
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  TextInput,
} from "../../components/ui";
import { TONE_TEXT } from "../../lib/status";
import { AllowedLoginsEditor } from "./AllowedLoginsEditor";
import {
  AllowedHostRow,
  DisableBody,
  EnableBody,
  FailureBlock,
  FunnelBlock,
  LINK,
  Row,
  SharedBlock,
  WarningList,
} from "./TailscaleBlocks";
import {
  conflictTitle,
  parsePort,
  parseServeFailure,
  shareControl,
  statusRows,
  type ServeFailure,
} from "./tailscaleView";

/** Element id of the card; `/settings/auth#tailscale` scrolls to it. */
export const TAILSCALE_CARD_ID = "tailscale";

/** Tailscale: what hal found, sharing on the tailnet, access (`allowed_hosts`, who can sign in) and warnings. */
export function TailscaleCard() {
  const ts = useTailscaleStatus();
  const overview = useAuthOverview();
  const refresh = useRefreshTailscale();
  const status = ts.data;
  return (
    <section
      id={TAILSCALE_CARD_ID}
      className="scroll-mt-4"
      data-testid="tailscale-section"
    >
      <Panel
        title="Tailscale"
        actions={
          status && (
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
          )
        }
      >
        {ts.isPending && <LoadingState />}
        {ts.isError && (
          <ErrorState error={ts.error} onRetry={() => void ts.refetch()} />
        )}
        {status && (
          <TailscaleDetails
            status={status}
            refreshError={refresh.isError ? refresh.error.message : null}
            logins={
              overview.isSuccess ? (
                <AllowedLoginsEditor
                  configured={overview.data.allowed_logins}
                  ownerLogin={status.owner_login}
                />
              ) : overview.isError ? (
                <ErrorState
                  error={overview.error}
                  onRetry={() => void overview.refetch()}
                />
              ) : (
                <LoadingState />
              )
            }
          />
        )}
      </Panel>
    </section>
  );
}

type Dialog = "enable" | "disable" | null;
type Conflict = Extract<ServeFailure, { kind: "conflict" }> & { port: number };

function Section({
  title,
  testId,
  children,
}: {
  title: string;
  testId?: string;
  children: ReactNode;
}) {
  return (
    <div
      className="space-y-2 border-t border-line pt-3 first:border-t-0 first:pt-0"
      data-testid={testId}
    >
      <h3 className="m-0 text-xs font-semibold">{title}</h3>
      {children}
    </div>
  );
}

function TailscaleDetails({
  status,
  refreshError,
  logins,
}: {
  status: TailscaleStatus;
  refreshError: string | null;
  /** The editor for who can sign in (or its loading state). */
  logins: ReactNode;
}) {
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
    <div className="space-y-4">
      <Section title="Status">
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
        {refreshError && (
          <p role="alert" className="m-0 text-xs text-bad">
            Could not check again: {refreshError}
          </p>
        )}
      </Section>

      <Section title="Share hal on your tailnet">
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
      </Section>

      <Section title="Access">
        <dl className="m-0 grid grid-cols-[11rem_1fr] gap-x-3 gap-y-2 text-xs">
          <AllowedHostRow status={status} />
          <Row label="Who can sign in" testId="tailscale-logins-row">
            {logins}
          </Row>
        </dl>
      </Section>

      {status.warnings.length > 0 && (
        <Section title="Warnings">
          <div data-testid="tailscale-warnings">
            <WarningList warnings={status.warnings} />
          </div>
        </Section>
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
