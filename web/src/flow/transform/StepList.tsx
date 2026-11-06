// The vertical list of step cards: collapse, delete, reorder (drag handle with
// HTML5 drag and drop, plus move up/down buttons) and the "Add step" menu.

import {
  useEffect,
  useRef,
  useState,
  type DragEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Icon, type IconName } from "../../components/icons";
import { Badge, Button } from "../../components/ui";
import { cx } from "../../lib/cx";
import { hasError, stepMessages, type StepMessage } from "./diagnostics";
import type { FieldInfo } from "./fields";
import { StepForm } from "./StepForms";
import {
  insertIndex,
  moveItem,
  newStep,
  normalizeStep,
  STEP_INFO,
  stepTitle,
} from "./steps";
import {
  STEP_KINDS,
  type StepKind,
  type TransformStep,
  type TransformStepResult,
} from "./types";

export interface StepListProps {
  steps: TransformStep[];
  /** Replace the steps. `key` merges keystroke edits into one undo step. */
  onChange: (steps: TransformStep[], key?: string) => void;
  /** `fieldLists[i]`: fields step i can read. */
  fieldLists: FieldInfo[][];
  /** Preview result per step; undefined where it no longer fits the steps. */
  results: (TransformStepResult | undefined)[];
  /** Plain-language sentence for step i. */
  describe: (i: number) => string;
  messages: Map<number, { field: string; msg: StepMessage }[]>;
}

let nextKey = 0;
const newKey = () => `step-${nextKey++}`;

const MIME = "application/x-hal9001-step";

function IconButton({
  icon,
  label,
  onClick,
  disabled,
  danger,
}: {
  icon: IconName;
  label: string;
  onClick: () => void;
  disabled?: boolean;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className={cx(
        "inline-flex size-6 shrink-0 items-center justify-center rounded text-muted hover:bg-raised disabled:cursor-not-allowed disabled:opacity-40",
        danger ? "hover:text-bad" : "hover:text-fg",
      )}
    >
      <Icon name={icon} size={14} />
    </button>
  );
}

function StepCard({
  index,
  total,
  step,
  subtitle,
  collapsed,
  fields,
  result,
  items,
  onToggle,
  onChange,
  onMove,
  onDelete,
  handle,
}: {
  index: number;
  total: number;
  step: TransformStep;
  subtitle: string;
  collapsed: boolean;
  fields: FieldInfo[];
  result: TransformStepResult | undefined;
  items: { field: string; msg: StepMessage }[] | undefined;
  onToggle: () => void;
  onChange: (next: TransformStep, key?: string) => void;
  onMove: (delta: number) => void;
  onDelete: () => void;
  handle: ReactNode;
}) {
  const msgs = stepMessages(step, items);
  const all = (items ?? []).map((i) => i.msg);
  const bad = hasError(all) || result?.error != null;
  const dropped = result?.dropped === true;
  const title = stepTitle(step);

  return (
    <div
      data-testid="step-card"
      className={cx(
        "rounded-md border bg-panel",
        bad ? "border-bad" : dropped ? "border-warn" : "border-line",
      )}
    >
      <div className="flex items-start gap-1 px-1.5 py-1.5">
        {handle}
        <button
          type="button"
          aria-expanded={!collapsed}
          aria-label={`${collapsed ? "Expand" : "Collapse"} step ${index + 1}: ${title}`}
          onClick={onToggle}
          className="flex min-w-0 flex-1 items-start gap-1.5 text-left"
        >
          <span
            className={cx(
              "mt-0.5 shrink-0 text-muted transition-transform",
              !collapsed && "rotate-90",
            )}
          >
            <Icon name="chevronRight" size={12} />
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-xs font-semibold">
              <span className="mr-1 font-normal text-muted">{index + 1}.</span>
              {title}
            </span>
            <span className="block text-[11px] text-muted">{subtitle}</span>
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-1 pt-0.5">
          {result && !dropped && result.error == null && (
            <span title="Items leaving this step">
              <Badge className="font-mono">→ {result.outputs_count}</Badge>
            </span>
          )}
          {dropped && <Badge tone="warn">nothing left</Badge>}
          {result?.error != null && <Badge tone="bad">failed</Badge>}
          {bad && collapsed && (
            <span className="text-bad" title="This step has a problem">
              <Icon name="error" size={14} />
            </span>
          )}
        </div>
        <IconButton
          icon="up"
          label={`Move step ${index + 1} up`}
          disabled={index === 0}
          onClick={() => onMove(-1)}
        />
        <IconButton
          icon="down"
          label={`Move step ${index + 1} down`}
          disabled={index === total - 1}
          onClick={() => onMove(1)}
        />
        <IconButton
          icon="trash"
          danger
          label={`Delete step ${index + 1}`}
          onClick={onDelete}
        />
      </div>
      {!collapsed && (
        <div className="space-y-2.5 border-t border-line p-2">
          <StepForm
            step={step}
            fields={fields}
            onChange={onChange}
            msgs={msgs.at}
          />
          {msgs.card.map((m, i) => (
            <p
              key={i}
              className={cx(
                "m-0 text-[11px]",
                m.severity === "error" ? "text-bad" : "text-warn",
              )}
            >
              {m.message}
            </p>
          ))}
          {result?.error != null && (
            <p className="m-0 font-mono text-[11px] text-bad">{result.error}</p>
          )}
        </div>
      )}
    </div>
  );
}

function AddStepMenu({
  steps,
  onAdd,
}: {
  steps: readonly TransformStep[];
  onAdd: (kind: StepKind) => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const hasRoute = steps.some((s) => s.type === "route");

  const items = () =>
    Array.from(
      root.current?.querySelectorAll<HTMLButtonElement>(
        "[role=menuitem]:not(:disabled)",
      ) ?? [],
    );

  useEffect(() => {
    if (open) items()[0]?.focus();
  }, [open]);

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape" && open) {
      e.stopPropagation();
      setOpen(false);
      root.current?.querySelector<HTMLButtonElement>("button")?.focus();
    } else if (open && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const list = items();
      const at = list.indexOf(document.activeElement as HTMLButtonElement);
      const next = e.key === "ArrowDown" ? at + 1 : at - 1;
      list[(next + list.length) % list.length]?.focus();
    }
  };

  return (
    <div
      ref={root}
      className="relative"
      onKeyDown={onKeyDown}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget)) setOpen(false);
      }}
    >
      <Button
        icon="plus"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        Add step
      </Button>
      {open && (
        <div
          role="menu"
          aria-label="Step kinds"
          className="absolute left-0 z-30 mt-1 max-h-96 w-80 overflow-auto rounded-md border border-line bg-panel py-1 shadow-lg"
        >
          {STEP_KINDS.map((kind) => {
            const blocked = kind === "route" && hasRoute;
            return (
              <button
                key={kind}
                type="button"
                role="menuitem"
                disabled={blocked}
                onClick={() => {
                  setOpen(false);
                  onAdd(kind);
                }}
                className="block w-full px-3 py-1.5 text-left hover:bg-raised focus:bg-raised disabled:cursor-not-allowed disabled:opacity-50"
              >
                <span className="block text-xs font-medium">
                  {STEP_INFO[kind].title}
                </span>
                <span className="block text-[11px] text-muted">
                  {blocked
                    ? "This node already has a route step."
                    : STEP_INFO[kind].description}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

export function StepList({
  steps,
  onChange,
  fieldLists,
  results,
  describe,
  messages,
}: StepListProps) {
  // Stable identity per step, kept aligned through moves, for collapse state.
  const [keys, setKeys] = useState<string[]>(() => steps.map(newKey));
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  const [armed, setArmed] = useState<number | null>(null);
  const [dragFrom, setDragFrom] = useState<number | null>(null);
  const [dropAt, setDropAt] = useState<number | null>(null);
  const list = useRef<HTMLDivElement>(null);
  const added = useRef<number | null>(null);

  let k = keys;
  if (k.length !== steps.length) {
    // The steps changed under us (undo, redo): keep leading keys, mint the rest.
    k = [...keys.slice(0, steps.length)];
    while (k.length < steps.length) k.push(newKey());
    setKeys(k);
  }

  useEffect(() => {
    if (added.current === null) return;
    list.current
      ?.querySelector(`[data-step="${added.current}"]`)
      ?.scrollIntoView({ block: "nearest" });
    added.current = null;
  }, [steps.length]);

  const edit = (i: number, next: TransformStep, key?: string) =>
    onChange(
      steps.map((s, j) => (j === i ? normalizeStep(next) : s)),
      key === undefined ? undefined : `steps[${i}].${key}`,
    );

  const move = (from: number, to: number) => {
    if (from === to || to < 0 || to >= steps.length) return;
    setKeys(moveItem(k, from, to));
    onChange(moveItem(steps, from, to));
  };

  const add = (kind: StepKind) => {
    const at = insertIndex(steps);
    const next = [...steps];
    next.splice(at, 0, newStep(kind));
    const nk = [...k];
    nk.splice(at, 0, newKey());
    setKeys(nk);
    added.current = at;
    onChange(next);
  };

  const remove = (i: number) => {
    setKeys(k.filter((_, j) => j !== i));
    onChange(steps.filter((_, j) => j !== i));
  };

  const toggle = (key: string) =>
    setCollapsed((cur) => {
      const next = new Set(cur);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  const onDragOver = (e: DragEvent<HTMLElement>, i: number) => {
    if (dragFrom === null) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const r = e.currentTarget.getBoundingClientRect();
    setDropAt(e.clientY < r.top + r.height / 2 ? i : i + 1);
  };

  const endDrag = () => {
    setArmed(null);
    setDragFrom(null);
    setDropAt(null);
  };

  return (
    <div className="space-y-2" ref={list} data-testid="step-list">
      {steps.length === 0 && (
        <p className="m-0 rounded-md border border-dashed border-line p-3 text-center text-xs text-muted">
          No steps yet. Add a step to start reshaping the item.
        </p>
      )}
      {steps.map((step, i) => (
        <div
          key={k[i]}
          data-step={i}
          draggable={armed === i}
          onDragStart={(e) => {
            e.dataTransfer.setData(MIME, String(i));
            e.dataTransfer.effectAllowed = "move";
            setDragFrom(i);
          }}
          onDragEnd={endDrag}
          onDragOver={(e) => onDragOver(e, i)}
          onDrop={(e) => {
            if (dragFrom === null || dropAt === null) return;
            e.preventDefault();
            move(dragFrom, dropAt > dragFrom ? dropAt - 1 : dropAt);
            endDrag();
          }}
          className={cx(
            "relative",
            dragFrom === i && "opacity-50",
            dropAt === i &&
              dragFrom !== null &&
              "before:absolute before:-top-1.5 before:inset-x-0 before:h-0.5 before:rounded before:bg-accent",
            dropAt === i + 1 &&
              i === steps.length - 1 &&
              "after:absolute after:-bottom-1.5 after:inset-x-0 after:h-0.5 after:rounded after:bg-accent",
          )}
        >
          <StepCard
            index={i}
            total={steps.length}
            step={step}
            subtitle={describe(i)}
            collapsed={collapsed.has(k[i])}
            fields={fieldLists[i] ?? []}
            result={results[i]}
            items={messages.get(i)}
            onToggle={() => toggle(k[i])}
            onChange={(next, key) => edit(i, next, key)}
            onMove={(d) => move(i, i + d)}
            onDelete={() => remove(i)}
            handle={
              <span
                title="Drag to reorder"
                aria-hidden="true"
                onMouseDown={() => setArmed(i)}
                onMouseUp={() => setArmed(null)}
                className="mt-0.5 inline-flex size-5 shrink-0 cursor-grab items-center justify-center rounded text-muted hover:bg-raised active:cursor-grabbing"
              >
                <Icon name="grip" size={14} />
              </span>
            }
          />
        </div>
      ))}
      <AddStepMenu steps={steps} onAdd={add} />
    </div>
  );
}
