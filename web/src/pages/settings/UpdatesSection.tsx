import { errorMessage } from "../../api/client";
import { useSettings, useUpdateSettings } from "../../api/settings";
import { useCheckUpdate, useUpdateStatus } from "../../api/update";
import {
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  Select,
  Toggle,
} from "../../components/ui";
import { formatTime, relativeTime } from "../../lib/format";

export function UpdatesSection() {
  const status = useUpdateStatus();
  const settings = useSettings();
  const save = useUpdateSettings();
  const check = useCheckUpdate();

  if (status.isPending || settings.isPending) return <LoadingState />;
  if (status.isError)
    return (
      <ErrorState error={status.error} onRetry={() => void status.refetch()} />
    );
  if (settings.isError)
    return (
      <ErrorState
        error={settings.error}
        onRetry={() => void settings.refetch()}
      />
    );
  const s = status.data;
  const { hash } = settings.data;
  const patch = (p: {
    update_channel?: "stable" | "beta";
    update_auto_install?: boolean;
  }) => save.mutate({ ...p, expected_hash: hash });

  return (
    <div className="space-y-4">
      {!s.enabled && (
        <div
          role="status"
          className="rounded-md border border-line bg-raised p-3 text-xs"
        >
          {s.disabled_reason ?? "Updates are disabled."}
        </div>
      )}
      <Panel title="Updates">
        <div className="space-y-3 text-xs">
          <div>
            Current: HAL9001 {s.current.version || "unknown"} (build{" "}
            {s.current.build})
          </div>
          <Field
            label="Channel"
            hint="Beta gets every build from main; stable gets builds promoted with a stable/v0.1.N tag."
          >
            <Select
              value={s.channel}
              disabled={save.isPending}
              onChange={(e) =>
                patch({ update_channel: e.target.value as "stable" | "beta" })
              }
            >
              <option value="stable">stable</option>
              <option value="beta">beta</option>
            </Select>
          </Field>
          <Toggle
            checked={s.auto_install_when_idle}
            disabled={save.isPending}
            onChange={(v) => patch({ update_auto_install: v })}
            label="Install updates automatically when idle"
          />
          {save.isError && (
            <div role="alert" className="text-bad">
              {errorMessage(save.error)}
            </div>
          )}
          <div>State: {s.state}</div>
          <div>
            Last checked:{" "}
            <span title={formatTime(s.last_checked_at)}>
              {relativeTime(s.last_checked_at)}
            </span>
          </div>
          {s.latest && (
            <div>
              Latest on {s.channel}: {s.latest.version} (build {s.latest.build})
              {s.latest.notes_url && (
                <>
                  {" "}
                  <a
                    className="underline"
                    href={s.latest.notes_url}
                    target="_blank"
                    rel="noreferrer"
                  >
                    Release notes
                  </a>
                </>
              )}
            </div>
          )}
          {s.error && <div className="text-bad">{s.error}</div>}
          <Button
            icon="refresh"
            disabled={!s.enabled || check.isPending}
            onClick={() => check.mutate()}
          >
            Check now
          </Button>
        </div>
      </Panel>
    </div>
  );
}
