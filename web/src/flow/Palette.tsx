import { Panel, useReactFlow, useStore } from "@xyflow/react";
import { useRef, type DragEvent } from "react";
import { Icon, type IconName } from "../components/icons";
import { cx } from "../lib/cx";
import { PALETTE_MIME, type PaletteDrag } from "./FlowCanvas";
import type { NodeType, UIPos } from "./model";

const NODE_ITEMS: {
  type: NodeType;
  label: string;
  icon: IconName;
  hint: string;
}[] = [
  {
    type: "command",
    label: "Command",
    icon: "command",
    hint: "Run a script on the payload",
  },
  {
    type: "transform",
    label: "Transform",
    icon: "transform",
    hint: "Filter, reshape or route the payload without writing code",
  },
  {
    type: "agent",
    label: "Agent",
    icon: "agent",
    hint: "Hand the event to a coding agent",
  },
  {
    type: "inbox",
    label: "Inbox note",
    icon: "note",
    hint: "Leave a note for yourself",
  },
];

export function Palette({
  sensors,
  subscribed,
  onAdd,
}: {
  /** All sensors defined on this daemon. */
  sensors: string[];
  subscribed: ReadonlySet<string>;
  onAdd: (item: PaletteDrag, position: UIPos) => void;
}) {
  const rf = useReactFlow();
  const size = useStore((s) => `${s.width}x${s.height}`);
  const added = useRef(0);

  const center = (): UIPos => {
    const [w, h] = size.split("x").map(Number);
    const { x, y, zoom } = rf.getViewport();
    // Fan successive adds out over a 3x2 grid so they do not stack.
    const k = added.current++ % 6;
    return {
      x: (w / 2 - x) / zoom - 120 + ((k % 3) - 1) * 280,
      y: (h / 2 - y) / zoom - 120 + Math.floor(k / 3) * 180,
    };
  };

  const onDragStart = (e: DragEvent, item: PaletteDrag) => {
    e.dataTransfer.setData(PALETTE_MIME, JSON.stringify(item));
    e.dataTransfer.effectAllowed = "copy";
  };

  const itemClass =
    "flex w-full items-center gap-2 rounded-md border border-line bg-panel px-2 py-1.5 text-left text-xs hover:bg-raised cursor-grab active:cursor-grabbing";

  return (
    <Panel
      position="top-left"
      className="!m-2 w-44 space-y-2 rounded-lg border border-line bg-panel/95 p-2 shadow-sm"
    >
      <div className="px-0.5 text-[10px] font-semibold uppercase tracking-wide text-muted">
        Nodes
      </div>
      <div className="space-y-1">
        {NODE_ITEMS.map((it) => (
          <button
            key={it.type}
            type="button"
            draggable
            className={itemClass}
            title={`${it.hint}. Drag onto the canvas, or press Enter to add.`}
            onDragStart={(e) => onDragStart(e, { kind: "node", type: it.type })}
            onClick={() => onAdd({ kind: "node", type: it.type }, center())}
          >
            <Icon name={it.icon} size={14} />
            {it.label}
          </button>
        ))}
      </div>
      <div className="px-0.5 pt-1 text-[10px] font-semibold uppercase tracking-wide text-muted">
        Sensors
      </div>
      <div className="max-h-40 space-y-1 overflow-y-auto">
        {sensors.length === 0 && (
          <div className="px-1 text-[11px] text-muted">No sensors defined.</div>
        )}
        {sensors.map((id) => {
          const on = subscribed.has(id);
          return (
            <button
              key={id}
              type="button"
              draggable={!on}
              disabled={on}
              className={cx(itemClass, on && "cursor-default opacity-50")}
              title={
                on ? "Already subscribed" : "Subscribe this flow to the sensor"
              }
              onDragStart={(e) => onDragStart(e, { kind: "sensor", id })}
              onClick={() => onAdd({ kind: "sensor", id }, center())}
            >
              <Icon name="sensor" size={14} />
              <span className="truncate">{id}</span>
              {on && <Icon name="check" size={12} className="ml-auto" />}
            </button>
          );
        })}
      </div>
    </Panel>
  );
}
