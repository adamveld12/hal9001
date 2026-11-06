// Form controls shared by the transform step editors: labelled field wrapper
// with inline messages, a field picker (combobox), typed value inputs and a
// list-of-values input.

import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Badge, Select, TextInput } from "../../components/ui";
import { cx } from "../../lib/cx";
import type { StepMessage } from "./diagnostics";
import type { FieldInfo } from "./fields";
import { coerceListItem, coerceText } from "./operators";
import type { Scalar } from "./types";

export function Messages({ list }: { list: readonly StepMessage[] }) {
  return (
    <>
      {list.map((m, i) => (
        <span
          key={i}
          className={cx(
            "block text-[11px]",
            m.severity === "error"
              ? "text-bad"
              : m.severity === "warning"
                ? "text-warn"
                : "text-muted",
          )}
        >
          {m.message}
        </span>
      ))}
    </>
  );
}

/** A labelled control with its hint and the diagnostics for that field. */
export function FormField({
  label,
  hint,
  msgs = [],
  children,
}: {
  label: string;
  hint?: ReactNode;
  msgs?: readonly StepMessage[];
  children: ReactNode;
}) {
  return (
    <div role="group" aria-label={label} className="space-y-1">
      <div className="text-[11px] font-medium text-muted">{label}</div>
      {children}
      {hint && msgs.length === 0 && (
        <span className="block text-[11px] text-muted">{hint}</span>
      )}
      <Messages list={msgs} />
    </div>
  );
}

const TYPE_TONE = {
  string: "info",
  number: "run",
  boolean: "warn",
  array: "ok",
  object: "muted",
  null: "muted",
  unknown: "muted",
} as const;

export function TypeBadge({ type }: { type: FieldInfo["type"] }) {
  return (
    <Badge tone={TYPE_TONE[type]} className="shrink-0 font-mono">
      {type}
    </Badge>
  );
}

/**
 * Text input with a dropdown of known fields (path, type, example from the
 * sample). Any text is accepted. `onChange(path, typing)` reports `typing`
 * for keystrokes so the editor can merge them into one undo step. With
 * `clearOnPick` it is an "add a field" box: it starts empty after each pick.
 */
export function FieldPicker({
  label,
  value,
  onChange,
  fields,
  placeholder = "Choose or type a field",
  invalid,
  clearOnPick,
}: {
  label: string;
  value: string;
  onChange: (path: string, typing: boolean) => void;
  fields: readonly FieldInfo[];
  placeholder?: string;
  invalid?: boolean;
  clearOnPick?: boolean;
}) {
  const listId = useId();
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [typed, setTyped] = useState(false);
  const [draft, setDraft] = useState("");
  const text = clearOnPick ? draft : value;
  const query = typed ? text.trim().toLowerCase() : "";
  const options = fields
    .filter(
      (f) =>
        query === "" ||
        f.path.toLowerCase().includes(query) ||
        f.example.toLowerCase().includes(query),
    )
    .slice(0, 100);

  useEffect(() => {
    if (active < 0) return;
    document
      .getElementById(`${listId}-${active}`)
      ?.scrollIntoView({ block: "nearest" });
  }, [active, listId]);

  const pick = (path: string, typing = false) => {
    onChange(path, typing);
    setOpen(false);
    setTyped(false);
    setActive(-1);
    if (clearOnPick) setDraft("");
  };

  const showList = open && (options.length > 0 || text.trim() !== "");

  return (
    <div className="relative">
      <TextInput
        role="combobox"
        aria-label={label}
        aria-expanded={showList}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={active >= 0 ? `${listId}-${active}` : undefined}
        autoComplete="off"
        spellCheck={false}
        className="font-mono"
        value={text}
        placeholder={placeholder}
        invalid={invalid}
        onFocus={() => setOpen(true)}
        onBlur={() => {
          setOpen(false);
          setTyped(false);
          setActive(-1);
          if (clearOnPick && draft.trim() !== "") pick(draft.trim());
        }}
        onChange={(e) => {
          setOpen(true);
          setTyped(true);
          setActive(-1);
          if (clearOnPick) setDraft(e.target.value);
          else onChange(e.target.value, true);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setOpen(true);
            setActive((i) => Math.min(options.length - 1, i + 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((i) => Math.max(0, i - 1));
          } else if (e.key === "Enter") {
            if (open && active >= 0 && options[active]) {
              e.preventDefault();
              pick(options[active].path);
            } else if (clearOnPick && draft.trim() !== "") {
              e.preventDefault();
              pick(draft.trim());
            }
          } else if (e.key === "Escape" && open) {
            e.preventDefault();
            e.stopPropagation();
            setOpen(false);
          }
        }}
      />
      {showList && (
        <ul
          id={listId}
          role="listbox"
          aria-label={`${label} suggestions`}
          className="absolute z-30 mt-1 max-h-56 w-full list-none overflow-auto rounded-md border border-line bg-panel p-0 shadow-lg"
        >
          {options.map((f, i) => (
            <li
              key={f.path}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              // mousedown keeps the input focused so the pick is not lost to blur
              onMouseDown={(e) => {
                e.preventDefault();
                pick(f.path);
              }}
              className={cx(
                "flex cursor-pointer items-center gap-2 px-2 py-1 text-xs",
                i === active ? "bg-raised" : "hover:bg-raised",
              )}
            >
              <span className="min-w-0 flex-1 truncate font-mono">
                {f.path}
              </span>
              <TypeBadge type={f.type} />
              <span className="max-w-[7rem] shrink-0 truncate text-muted">
                {f.example}
              </span>
            </li>
          ))}
          {options.length === 0 && (
            <li className="px-2 py-1.5 text-[11px] text-muted">
              {fields.length === 0
                ? "No sample yet. Type a path such as author.login."
                : `No field matches. “${text.trim()}” is used as typed.`}
            </li>
          )}
        </ul>
      )}
    </div>
  );
}

/** A number box that keeps what is being typed ("1.", "-") until it is a number. */
function NumberBox({
  label,
  value,
  onChange,
  invalid,
  placeholder,
}: {
  label: string;
  value: number | undefined;
  onChange: (v: number | undefined) => void;
  invalid?: boolean;
  placeholder?: string;
}) {
  const [draft, setDraft] = useState(value === undefined ? "" : String(value));
  const [seen, setSeen] = useState(value);
  // Follow outside changes (undo, another edit) but keep half-typed text such as "1.".
  if (value !== seen) {
    setSeen(value);
    if (coerceText(draft, "number") !== value) {
      setDraft(value === undefined ? "" : String(value));
    }
  }
  return (
    <TextInput
      aria-label={label}
      inputMode="decimal"
      value={draft}
      invalid={invalid}
      placeholder={placeholder}
      onChange={(e) => {
        setDraft(e.target.value);
        const n = coerceText(e.target.value, "number");
        onChange(typeof n === "number" ? n : undefined);
      }}
    />
  );
}

export type ValueTag = "string" | "number" | "boolean" | "null";

const TAG_LABEL: Record<ValueTag, string> = {
  string: "Text",
  number: "Number",
  boolean: "True / false",
  null: "Nothing",
};

function tagOf(v: unknown): ValueTag | undefined {
  if (v === null) return "null";
  if (typeof v === "string") return "string";
  if (typeof v === "number") return "number";
  if (typeof v === "boolean") return "boolean";
  return undefined;
}

function initialFor(tag: ValueTag, from: unknown): unknown {
  if (tag === "null") return null;
  if (from !== undefined && from !== null) {
    const c = coerceText(String(from), tag);
    if (c !== undefined) return c;
  }
  return tag === "string" ? "" : tag === "boolean" ? true : undefined;
}

/**
 * One JSON scalar with a type selector when `tags` allows more than one.
 * `onChange(value, typing)` gets `undefined` while a number box is empty.
 */
export function TypedValueInput({
  label,
  value,
  onChange,
  tags,
  invalid,
  placeholder,
}: {
  label: string;
  value: unknown;
  onChange: (v: unknown, typing: boolean) => void;
  tags: readonly ValueTag[];
  invalid?: boolean;
  placeholder?: string;
}) {
  const own = tagOf(value);
  const current: ValueTag = own && tags.includes(own) ? own : tags[0];
  return (
    <div className="flex gap-1.5">
      {tags.length > 1 && (
        <Select
          aria-label={`${label}: type`}
          className="w-28 shrink-0"
          value={current}
          onChange={(e) =>
            onChange(initialFor(e.target.value as ValueTag, value), false)
          }
        >
          {tags.map((t) => (
            <option key={t} value={t}>
              {TAG_LABEL[t]}
            </option>
          ))}
        </Select>
      )}
      <div className="min-w-0 flex-1">
        {current === "string" && (
          <TextInput
            aria-label={label}
            value={typeof value === "string" ? value : ""}
            invalid={invalid}
            placeholder={placeholder}
            onChange={(e) => onChange(e.target.value, true)}
          />
        )}
        {current === "number" && (
          <NumberBox
            label={label}
            value={typeof value === "number" ? value : undefined}
            invalid={invalid}
            placeholder={placeholder ?? "0"}
            onChange={(n) => onChange(n, true)}
          />
        )}
        {current === "boolean" && (
          <Select
            aria-label={label}
            value={typeof value === "boolean" ? String(value) : ""}
            invalid={invalid}
            onChange={(e) => onChange(e.target.value === "true", false)}
          >
            {typeof value !== "boolean" && <option value="">Choose…</option>}
            <option value="true">true</option>
            <option value="false">false</option>
          </Select>
        )}
        {current === "null" && (
          <span className="block py-1 text-xs text-muted">nothing (null)</span>
        )}
      </div>
    </div>
  );
}

/** A list of values as removable chips; Enter or a comma adds what is typed. */
export function ChipsInput({
  label,
  values,
  onChange,
  fieldType,
  invalid,
  placeholder = "Type a value, press Enter",
}: {
  label: string;
  values: readonly Scalar[];
  onChange: (values: Scalar[]) => void;
  fieldType: FieldInfo["type"];
  invalid?: boolean;
  placeholder?: string;
}) {
  const [draft, setDraft] = useState("");
  const [bad, setBad] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);

  const add = (text: string): boolean => {
    const parts = text.split(",").filter((p) => p.trim() !== "");
    if (parts.length === 0) return true;
    const parsed = parts.map((p) => coerceListItem(p, fieldType));
    if (parsed.some((v) => v === undefined)) {
      setBad(
        fieldType === "number"
          ? "Use numbers only."
          : fieldType === "boolean"
            ? "Use true or false."
            : "Not a valid value.",
      );
      return false;
    }
    setBad(null);
    onChange([...values, ...(parsed as Scalar[])]);
    return true;
  };

  return (
    <div className="space-y-1">
      <div
        className={cx(
          "flex min-h-7 flex-wrap items-center gap-1 rounded-md border bg-panel px-1.5 py-1",
          invalid || bad ? "border-bad" : "border-line",
        )}
      >
        {values.map((v, i) => (
          <span
            key={`${i}:${String(v)}`}
            className="inline-flex items-center gap-1 rounded border border-line bg-raised px-1.5 text-[11px] leading-5"
          >
            <span
              className={cx("font-mono", typeof v !== "string" && "text-run")}
            >
              {String(v)}
            </span>
            <button
              type="button"
              aria-label={`Remove ${String(v)}`}
              className="text-muted hover:text-bad"
              onClick={() => onChange(values.filter((_, j) => j !== i))}
            >
              ×
            </button>
          </span>
        ))}
        <input
          ref={input}
          aria-label={label}
          className="min-w-[8ch] flex-1 bg-transparent py-0.5 text-xs outline-none placeholder:text-muted/70"
          value={draft}
          placeholder={values.length === 0 ? placeholder : ""}
          onChange={(e) => {
            const v = e.target.value;
            if (v.includes(",")) {
              if (add(v)) setDraft("");
              else setDraft(v);
            } else {
              setDraft(v);
              setBad(null);
            }
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && draft.trim() !== "") {
              e.preventDefault();
              if (add(draft)) setDraft("");
            } else if (
              e.key === "Backspace" &&
              draft === "" &&
              values.length > 0
            ) {
              onChange(values.slice(0, -1));
            }
          }}
          onBlur={() => {
            if (draft.trim() !== "" && add(draft)) setDraft("");
          }}
        />
      </div>
      {bad && <span className="block text-[11px] text-bad">{bad}</span>}
    </div>
  );
}
