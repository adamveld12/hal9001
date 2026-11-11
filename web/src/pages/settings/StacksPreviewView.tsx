import { useMemo } from "react";
import type { AuthStacksPreview } from "../../api/auth";
import { Badge } from "../../components/ui";
import { DiffView } from "../approvals/DiffView";
import { lineDiff } from "../approvals/diff";

const SEVERITY_TONE = { error: "bad", warning: "warn", info: "info" } as const;

/** What the server says about a stacks change: findings, what saving will ask, and the diff of the `auth:` section. */
export function StacksPreviewView({ preview }: { preview: AuthStacksPreview }) {
  const lines = useMemo(
    () => lineDiff(preview.before, preview.after),
    [preview.before, preview.after],
  );
  return (
    <div className="space-y-3 text-xs" data-testid="stacks-preview">
      {preview.diagnostics.length > 0 && (
        <ul
          className="m-0 list-none space-y-1 p-0"
          data-testid="stacks-diagnostics"
        >
          {preview.diagnostics.map((d, i) => (
            <li key={i} className="flex items-start gap-2">
              <Badge tone={SEVERITY_TONE[d.severity] ?? "muted"}>
                {d.severity}
              </Badge>
              <span>
                {d.path && <code className="font-mono">{d.path}: </code>}
                {d.message}
              </span>
            </li>
          ))}
        </ul>
      )}
      {!preview.valid && (
        <p className="m-0 font-medium text-bad" role="alert">
          Fix the errors above before saving.
        </p>
      )}
      {preview.valid && preview.confirm_required && (
        <div className="space-y-1" data-testid="stacks-confirm-reasons">
          <p className="m-0 font-medium">Saving will ask you to confirm:</p>
          <ul className="m-0 list-disc space-y-0.5 pl-4">
            {preview.reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
          {preview.ends_session && (
            <p className="m-0 font-bold">
              This ends your current session; you will need to sign in again.
            </p>
          )}
        </div>
      )}
      {preview.changed ? (
        <DiffView diff={{ path: "config.yaml: auth", lines }} />
      ) : (
        <p className="m-0 text-muted">
          No changes: config.yaml already says this.
        </p>
      )}
    </div>
  );
}
