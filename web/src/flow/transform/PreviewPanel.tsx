// Result of the live preview: decision, item counts along the steps, outputs
// per port, the compiled jq and the "Convert to code node" action.

import { useMemo, useState } from "react";
import { errorMessage } from "../../api/client";
import type { PreviewAnswer } from "../../api/transform";
import { SensorItemsTable } from "../../components/SensorItemsTable";
import { CopyButton } from "../../components/CopyButton";
import { Badge, Button, StatusBadge } from "../../components/ui";
import { cx } from "../../lib/cx";
import { ConvertDialog } from "./ConvertDialog";
import { countTrail, groupOutputs } from "./preview";
import type { TransformStep } from "./types";

function PortOutput({
  port,
  payloads,
  note,
}: {
  port: string;
  payloads: unknown[];
  note?: string | null;
}) {
  const items = useMemo(() => payloads.map((item) => ({ item })), [payloads]);
  return (
    <section aria-label={`Output ${port}`} className="space-y-1.5">
      <h4 className="m-0 flex items-center gap-1.5 text-xs font-semibold">
        <span className="font-mono">{port}</span>
        <Badge>
          {payloads.length} item{payloads.length === 1 ? "" : "s"}
        </Badge>
      </h4>
      {note && <p className="m-0 text-[11px] text-muted">{note}</p>}
      <SensorItemsTable items={items} maxHeight="16rem" />
    </section>
  );
}

export function PreviewPanel({
  steps,
  hasInput,
  answer,
  stale,
  updating,
  error,
  blockedAt,
  ports,
  onConvert,
}: {
  steps: TransformStep[];
  hasInput: boolean;
  answer: PreviewAnswer | undefined;
  /** The answer no longer fits the current steps or input. */
  stale: boolean;
  /** A new preview is on its way (shown only after a short delay). */
  updating: boolean;
  error: unknown;
  /** Index of the first step the client still sees as incomplete. */
  blockedAt: number | null;
  /** The node's output ports, in order. */
  ports: string[];
  onConvert: (jq: string) => void;
}) {
  const [converting, setConverting] = useState(false);
  const preview = answer?.preview;
  const groups = useMemo(
    () => (preview ? groupOutputs(preview.outputs, ports) : []),
    [preview, ports],
  );

  let body;
  if (steps.length === 0) {
    body = (
      <p className="m-0 text-xs text-muted">Add a step to see the result.</p>
    );
  } else if (!hasInput) {
    body = (
      <p className="m-0 text-xs text-muted">
        Choose or paste a sample above to see the result.
      </p>
    );
  } else if (blockedAt === 0) {
    body = (
      <p className="m-0 text-xs text-muted">Finish step 1 to see the result.</p>
    );
  } else if (error != null && !preview) {
    body = (
      <p role="alert" className="m-0 text-xs text-bad">
        The preview failed: {errorMessage(error)}
      </p>
    );
  } else if (!preview) {
    body = <p className="m-0 text-xs text-muted">Running the steps…</p>;
  } else {
    body = (
      <div
        className={cx("space-y-3 transition-opacity", stale && "opacity-60")}
        aria-busy={updating}
      >
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status={preview.decision} />
          <span
            className="font-mono text-[11px] text-muted"
            title="Items after each step, starting with the input"
          >
            {countTrail(preview.step_results).join(" → ")}
          </span>
          {updating && (
            <span className="text-[11px] text-muted">updating…</span>
          )}
        </div>
        {blockedAt !== null && (
          <p className="m-0 text-[11px] text-warn">
            Showing the steps before step {blockedAt + 1}; it still needs
            attention.
          </p>
        )}
        {error != null && (
          <p role="alert" className="m-0 text-[11px] text-bad">
            Could not refresh: {errorMessage(error)}
          </p>
        )}
        {preview.decision === "dropped" && (
          <div
            role="status"
            className="rounded-md border border-warn/40 bg-warn/10 p-2 text-xs"
          >
            <div className="font-semibold text-warn">Nothing comes out</div>
            {preview.reason && <div>{preview.reason}</div>}
          </div>
        )}
        {preview.decision === "error" && (
          <div
            role="alert"
            className="rounded-md border border-bad/40 bg-bad/10 p-2 text-xs"
          >
            <div className="font-semibold text-bad">The steps failed</div>
            {preview.error && (
              <div className="font-mono text-[11px]">{preview.error}</div>
            )}
            {!preview.error && preview.diagnostics.length > 0 && (
              <div>Some steps have problems; see the highlighted steps.</div>
            )}
          </div>
        )}
        {groups.map((g, i) => (
          <PortOutput
            key={g.port}
            port={g.port}
            payloads={g.payloads}
            note={
              i === 0 && preview.decision === "emitted" ? preview.reason : null
            }
          />
        ))}
      </div>
    );
  }

  return (
    <section className="space-y-2" aria-label="Preview result">
      <h3 className="m-0 text-xs font-semibold">Result</h3>
      {body}
      {preview && preview.jq !== "" && (
        <details>
          <summary className="cursor-pointer select-none text-[11px] font-medium text-muted">
            Show jq
          </summary>
          <div className="mt-1.5 space-y-1.5">
            <pre className="m-0 max-h-60 overflow-auto rounded-md border border-line bg-raised p-2 font-mono text-[11px]">
              {preview.jq}
            </pre>
            <CopyButton text={preview.jq} label="Copy jq" />
          </div>
        </details>
      )}
      <Button
        icon="code"
        disabled={steps.length === 0}
        onClick={() => setConverting(true)}
      >
        Convert to code node
      </Button>
      {converting && (
        <ConvertDialog
          steps={steps}
          onClose={() => setConverting(false)}
          onConverted={(jq) => {
            setConverting(false);
            onConvert(jq);
          }}
        />
      )}
    </section>
  );
}
