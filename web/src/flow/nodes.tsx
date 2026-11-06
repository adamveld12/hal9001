import { Handle, Position, type NodeProps } from "@xyflow/react";
import type { ReactNode } from "react";
import { Icon, type IconName } from "../components/icons";
import { cx } from "../lib/cx";
import { TONE_BADGE } from "../lib/status";
import { useCanvasContext } from "./canvasContext";
import { IN_HANDLE, type FlowGraphNode, type SensorGraphNode } from "./graph";
import {
  allPorts,
  isImplicitPort,
  type Diagnostic,
  type FlowNode,
  type TransformNode,
} from "./model";
import { VISUAL_LABEL, VISUAL_TONE } from "./overlay";
import { summarizeStep } from "./transform/steps";
import { worstSeverity } from "./validate";

const TYPE_ICON: Record<FlowNode["type"], IconName> = {
  command: "command",
  agent: "agent",
  inbox: "note",
  transform: "transform",
};

const TYPE_ACCENT: Record<FlowNode["type"], string> = {
  command: "text-info",
  agent: "text-run",
  inbox: "text-warn",
  transform: "text-ok",
};

function summary(node: FlowNode): string {
  switch (node.type) {
    case "command": {
      if (node.lang === "exec") return (node.command ?? []).join(" ") || "exec";
      const first = (node.script ?? "")
        .split("\n")
        .find((l) => l.trim() !== "");
      return `${node.lang}: ${first?.trim() ?? ""}`;
    }
    case "agent":
      return [
        node.runtime,
        node.mode === "approve"
          ? `approve/${node.approve ?? "proposal"}`
          : node.mode,
        node.prompt ?? "inline prompt",
      ]
        .filter(Boolean)
        .join(" · ");
    case "inbox":
      return node.title;
    case "transform": {
      const n = node.steps?.length ?? 0;
      return `${n} step${n === 1 ? "" : "s"}`;
    }
  }
}

function DiagnosticBadge({ diags }: { diags: Diagnostic[] }) {
  const sev = worstSeverity(diags);
  if (!sev) return null;
  return (
    <span
      title={diags.map((d) => d.message).join("\n")}
      className={cx(
        "ml-auto inline-flex items-center gap-1 rounded border px-1 text-[10px]",
        sev === "error" ? TONE_BADGE.bad : TONE_BADGE.warn,
      )}
    >
      <Icon name={sev === "error" ? "error" : "warning"} size={11} />
      {diags.length}
    </span>
  );
}

const BORDER_BY_TONE = {
  ok: "border-ok",
  warn: "border-warn",
  bad: "border-bad",
  info: "border-info",
  run: "border-run",
  muted: "border-line",
} as const;

function Card({
  id,
  selected,
  icon,
  accent,
  title,
  subtitle,
  children,
  left,
}: {
  id: string;
  selected: boolean;
  icon: IconName;
  accent: string;
  title: string;
  subtitle?: string;
  children?: ReactNode;
  left?: ReactNode;
}) {
  const ctx = useCanvasContext();
  const diags = ctx.diagnostics.get(id) ?? [];
  const sev = worstSeverity(diags);
  const ov = ctx.overlay?.nodes[id];
  const overlayActive = ctx.overlay !== undefined;
  const tone = ov ? VISUAL_TONE[ov.visual] : null;
  const dim = overlayActive && !ov;

  return (
    <div
      className={cx(
        "w-60 rounded-md border bg-panel text-xs shadow-sm",
        selected ? "ring-2 ring-accent" : "",
        tone
          ? BORDER_BY_TONE[tone]
          : sev === "error"
            ? "border-bad"
            : sev === "warning"
              ? "border-warn"
              : "border-line",
        ov?.visual === "running" ? "animate-pulse" : "",
        dim ? "opacity-50" : "",
      )}
      data-node-id={id}
      data-visual={ov?.visual}
    >
      {left}
      <div className="flex items-center gap-1.5 border-b border-line px-2 py-1.5">
        <span className={accent}>
          <Icon name={icon} size={14} />
        </span>
        <span className="truncate font-medium">{title}</span>
        {ov ? (
          <span
            className={cx(
              "ml-auto shrink-0 rounded border px-1 text-[10px]",
              TONE_BADGE[VISUAL_TONE[ov.visual]],
            )}
          >
            {VISUAL_LABEL[ov.visual]}
          </span>
        ) : (
          <DiagnosticBadge diags={diags} />
        )}
      </div>
      {subtitle !== undefined && (
        <div
          className="truncate px-2 py-1 font-mono text-[11px] text-muted"
          title={subtitle}
        >
          {subtitle || "\u00a0"}
        </div>
      )}
      {ov?.detail && (
        <div className="truncate px-2 pb-1 text-[11px] text-muted">
          {ov.detail}
        </div>
      )}
      {children}
    </div>
  );
}

function PortRows({ node }: { node: FlowNode }) {
  const ctx = useCanvasContext();
  const valid = allPorts(node);
  const used = ctx.usedPorts.get(node.id) ?? new Set<string>();
  // Ports in use by edges but not valid are still drawn (red) so the edge stays visible.
  const invalid = [...used].filter((p) => !valid.includes(p));
  const rows = [...valid, ...invalid];
  return (
    <div className="border-t border-line py-0.5">
      {rows.map((port) => {
        const bad = invalid.includes(port);
        const implicit = isImplicitPort(node, port);
        return (
          <div
            key={port}
            className="relative flex h-5 items-center justify-end pr-3"
          >
            <span
              className={cx(
                "font-mono text-[10px]",
                bad ? "text-bad" : implicit ? "text-muted" : "text-fg",
                port === "error" && !bad ? "text-bad/80" : "",
                port === "denied" && !bad ? "text-warn" : "",
              )}
              title={
                bad
                  ? "not a declared port"
                  : implicit
                    ? "implicit port"
                    : "declared port"
              }
            >
              {port}
            </span>
            <Handle
              type="source"
              position={Position.Right}
              id={port}
              isConnectable={!ctx.readOnly}
              className={cx(
                port === "error" && "!bg-bad",
                port === "denied" && "!bg-warn",
                bad && "!bg-bad",
              )}
            />
          </div>
        );
      })}
    </div>
  );
}

function InHandle() {
  const ctx = useCanvasContext();
  return (
    <Handle
      type="target"
      position={Position.Left}
      id={IN_HANDLE}
      isConnectable={!ctx.readOnly}
      style={{ top: 18 }}
    />
  );
}
const SUMMARY_STEPS = 3;

/** The first few steps in plain language, under the node title. */
function StepSummaries({ node }: { node: TransformNode }) {
  const steps = node.steps ?? [];
  if (steps.length === 0) {
    return <div className="px-2 pb-1 text-[11px] text-muted">No steps yet</div>;
  }
  return (
    <ol className="m-0 list-none space-y-0.5 px-2 pb-1 text-[11px]">
      {steps.slice(0, SUMMARY_STEPS).map((s, i) => (
        <li key={i} className="truncate" title={summarizeStep(s)}>
          <span className="mr-1 text-muted">{i + 1}.</span>
          {summarizeStep(s)}
        </li>
      ))}
      {steps.length > SUMMARY_STEPS && (
        <li className="text-muted">+{steps.length - SUMMARY_STEPS} more</li>
      )}
    </ol>
  );
}

export function FlowNodeView({ id, data, selected }: NodeProps<FlowGraphNode>) {
  const node = data.node;
  return (
    <Card
      id={id}
      selected={selected}
      icon={TYPE_ICON[node.type]}
      accent={TYPE_ACCENT[node.type]}
      title={node.id}
      subtitle={summary(node)}
      left={<InHandle />}
    >
      {node.type === "transform" && <StepSummaries node={node} />}
      <PortRows node={node} />
    </Card>
  );
}

export function SensorNodeView({
  id,
  data,
  selected,
}: NodeProps<SensorGraphNode>) {
  const ctx = useCanvasContext();
  const missing =
    ctx.knownSensors !== undefined && !ctx.knownSensors.has(data.sensor);
  const diags = ctx.diagnostics.get(id) ?? [];
  return (
    <div
      className={cx(
        "flex w-52 items-center gap-1.5 rounded-full border bg-raised px-2.5 py-1.5 text-xs",
        selected ? "ring-2 ring-accent" : "",
        missing || worstSeverity(diags) === "error"
          ? "border-bad"
          : "border-line",
      )}
      data-node-id={id}
    >
      <span className="text-ok">
        <Icon name="sensor" size={14} />
      </span>
      <span
        className="truncate font-medium"
        title={missing ? "sensor not found" : data.sensor}
      >
        {data.sensor}
      </span>
      {missing && (
        <span
          className={cx(
            "ml-auto rounded border px-1 text-[10px]",
            TONE_BADGE.bad,
          )}
        >
          missing
        </span>
      )}
      <Handle
        type="source"
        position={Position.Right}
        id="out"
        isConnectable={false}
        style={{ top: "50%" }}
      />
    </div>
  );
}
