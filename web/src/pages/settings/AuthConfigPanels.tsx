import { useTailscaleStatus } from "../../api/auth";
import { useSettings } from "../../api/settings";
import {
  Badge,
  Button,
  ErrorState,
  LoadingState,
  Panel,
} from "../../components/ui";
import { stackViews } from "./authView";

const CONTROL_TONE = {
  required: "warn",
  requisite: "bad",
  sufficient: "ok",
  optional: "muted",
} as const;

/** `auth.stacks` and `auth.services` from config.yaml, read-only. Secrets arrive redacted. */
export function AuthStackPanel() {
  const settings = useSettings();
  return (
    <Panel title="Auth stacks">
      {settings.isPending && <LoadingState />}
      {settings.isError && (
        <ErrorState
          error={settings.error}
          onRetry={() => void settings.refetch()}
        />
      )}
      {settings.isSuccess && (
        <div className="space-y-4">
          {stackViews(settings.data.config.auth).map((stack) => (
            <div key={stack.name}>
              <div className="mb-1 flex items-center gap-2 text-xs">
                <span className="font-mono font-medium">{stack.name}</span>
                <span className="text-muted">guards</span>
                {stack.services.length === 0 ? (
                  <span className="text-muted">no service</span>
                ) : (
                  stack.services.map((s) => <Badge key={s}>{s}</Badge>)
                )}
              </div>
              <ol className="m-0 list-none space-y-1 p-0">
                {stack.entries.map((e, i) => (
                  <li
                    key={`${i}-${e.module}`}
                    className="rounded-md border border-line px-2 py-1.5 text-xs"
                  >
                    <div className="flex items-center gap-2">
                      <span className="tabular-nums text-muted">{i + 1}.</span>
                      <span className="font-mono">{e.module}</span>
                      <Badge
                        tone={
                          CONTROL_TONE[
                            e.control as keyof typeof CONTROL_TONE
                          ] ?? "muted"
                        }
                      >
                        {e.control}
                      </Badge>
                    </div>
                    {e.options.length > 0 && (
                      <dl className="m-0 mt-1 grid grid-cols-[10rem_1fr] gap-x-3 pl-5 font-mono text-[11px]">
                        {e.options.map(([k, v]) => (
                          <div key={k} className="contents">
                            <dt className="text-muted">{k}</dt>
                            <dd className="m-0 break-all">{v}</dd>
                          </div>
                        ))}
                      </dl>
                    )}
                  </li>
                ))}
                {stack.entries.length === 0 && (
                  <li className="text-xs text-muted">
                    Not defined in config.yaml: no module can succeed, so access
                    is denied.
                  </li>
                )}
              </ol>
            </div>
          ))}
          <p className="m-0 text-[11px] text-muted">
            Edit in config.yaml; changes reload automatically.
          </p>
        </div>
      )}
    </Panel>
  );
}

export function TailscalePanel() {
  const ts = useTailscaleStatus();
  return (
    <Panel
      title="Tailscale"
      actions={
        <Button icon="refresh" onClick={() => void ts.refetch()}>
          Check
        </Button>
      }
    >
      {ts.isPending && <LoadingState />}
      {ts.isError && (
        <ErrorState error={ts.error} onRetry={() => void ts.refetch()} />
      )}
      {ts.isSuccess && !ts.data.available && (
        <p className="m-0 text-xs text-muted">
          Tailscale is not available
          {ts.data.error ? `: ${ts.data.error}` : "."}
        </p>
      )}
      {ts.isSuccess && ts.data.available && (
        <div className="space-y-3">
          <dl className="m-0 grid grid-cols-[10rem_1fr] gap-x-3 gap-y-1.5 text-xs">
            <dt className="text-muted">State</dt>
            <dd className="m-0">{ts.data.backend_state ?? "–"}</dd>
            <dt className="text-muted">Hostname</dt>
            <dd className="m-0 font-mono">{ts.data.hostname ?? "–"}</dd>
            <dt className="text-muted">Owner</dt>
            <dd className="m-0">{ts.data.owner_login ?? "–"}</dd>
            <dt className="text-muted">Serve to daemon port</dt>
            <dd className="m-0">
              <Badge tone={ts.data.serve_configured ? "ok" : "muted"}>
                {ts.data.serve_configured ? "configured" : "not configured"}
              </Badge>
            </dd>
            <dt className="text-muted">Hostname in allowed_hosts</dt>
            <dd className="m-0">
              <Badge tone={ts.data.host_allowed ? "ok" : "warn"}>
                {ts.data.host_allowed ? "yes" : "no"}
              </Badge>
            </dd>
            <dt className="text-muted">Allowed logins</dt>
            <dd className="m-0 font-mono">
              {ts.data.allowed_logins.length > 0
                ? ts.data.allowed_logins.join(", ")
                : "–"}
            </dd>
          </dl>
          {ts.data.funnel_enabled && (
            <p role="alert" className="m-0 text-xs text-bad">
              Funnel is on: something is exposed to the internet. Turn it off
              with `tailscale funnel reset`.
            </p>
          )}
          {ts.data.serve_configured && !ts.data.host_allowed && (
            <p className="m-0 text-xs text-warn">
              Requests through Tailscale are rejected until the hostname is in
              allowed_hosts in config.yaml.
            </p>
          )}
          {!ts.data.serve_configured && (
            <p className="m-0 text-[11px] text-muted">
              hal9001 never runs `tailscale serve` itself. To publish on your
              tailnet: `tailscale serve --bg --https=443 http://127.0.0.1:9001`.
            </p>
          )}
        </div>
      )}
    </Panel>
  );
}
