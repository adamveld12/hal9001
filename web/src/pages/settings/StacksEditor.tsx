import { useState } from "react";
import { Badge, Button, Select, TextInput } from "../../components/ui";
import {
  addEntry,
  addStack,
  CONTROLS,
  draftIssues,
  MODULES,
  moveEntry,
  optionRows,
  removeEntry,
  removeStack,
  renameStack,
  SERVICE_NAMES,
  setEntryControl,
  setEntryModule,
  setService,
  stackUsedBy,
  type Draft,
  type DraftEntry,
  type DraftStack,
} from "./authStacksDraft";

/** The values for a select: the known ones, plus the current one when it is none of them. */
function choices(known: readonly string[], current: string): string[] {
  return known.includes(current) ? [...known] : [...known, current];
}

/**
 * Editor for `auth.stacks` and `auth.services`. It only reshapes the draft (`onChange` gets the
 * edit); the server validates it when the user reviews the change.
 */
export function StacksEditor({
  draft,
  disabled,
  onChange,
}: {
  draft: Draft;
  disabled?: boolean;
  onChange: (edit: (d: Draft) => Draft) => void;
}) {
  const issues = draftIssues(draft);
  return (
    <div className="space-y-4" data-testid="stacks-editor">
      <div className="space-y-3">
        {draft.stacks.map((stack) => (
          <StackBlock
            key={stack.key}
            stack={stack}
            draft={draft}
            disabled={disabled}
            onChange={onChange}
          />
        ))}
        <Button
          icon="plus"
          disabled={disabled}
          data-testid="stacks-add-stack"
          onClick={() => onChange(addStack)}
        >
          Add stack
        </Button>
      </div>

      <fieldset className="m-0 space-y-2 border-0 p-0" disabled={disabled}>
        <legend className="mb-1 p-0 text-xs font-semibold">
          Which stack guards each service
        </legend>
        <div className="grid max-w-md grid-cols-[6rem_1fr] items-center gap-x-3 gap-y-1.5 text-xs">
          {SERVICE_NAMES.map((service) => (
            <ServiceRow
              key={service}
              service={service}
              draft={draft}
              onChange={onChange}
            />
          ))}
        </div>
      </fieldset>

      {issues.length > 0 && (
        <ul
          className="m-0 list-disc space-y-0.5 pl-4 text-xs text-bad"
          role="alert"
        >
          {issues.map((i) => (
            <li key={i}>{i}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

function ServiceRow({
  service,
  draft,
  onChange,
}: {
  service: (typeof SERVICE_NAMES)[number];
  draft: Draft;
  onChange: (edit: (d: Draft) => Draft) => void;
}) {
  const names = [...new Set(draft.stacks.map((s) => s.name))];
  const current = draft.services[service];
  return (
    <>
      <label htmlFor={`service-${service}`} className="font-mono">
        {service}
      </label>
      <Select
        id={`service-${service}`}
        value={current}
        onChange={(e) =>
          onChange((d) => setService(d, service, e.target.value))
        }
      >
        {choices(names, current).map((n) => (
          <option key={n} value={n}>
            {names.includes(n) ? n : `${n} (not defined)`}
          </option>
        ))}
      </Select>
    </>
  );
}

function StackBlock({
  stack,
  draft,
  disabled,
  onChange,
}: {
  stack: DraftStack;
  draft: Draft;
  disabled?: boolean;
  onChange: (edit: (d: Draft) => Draft) => void;
}) {
  const [nameText, setNameText] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);
  const users = stackUsedBy(draft, stack.name);

  const commitName = () => {
    if (nameText === null) return;
    const result = renameStack(draft, stack.key, nameText);
    setNameError(result.error);
    if (result.error === null) {
      setNameText(null);
      onChange((d) => renameStack(d, stack.key, nameText).draft);
    }
  };

  return (
    <section
      className="rounded-md border border-line"
      aria-label={`Stack ${stack.name}`}
      data-testid={`stack-${stack.name}`}
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-line bg-raised px-2 py-1.5 text-xs">
        <TextInput
          aria-label={`Name of stack ${stack.name}`}
          aria-invalid={nameError !== null}
          className="w-40 font-mono"
          value={nameText ?? stack.name}
          invalid={nameError !== null}
          disabled={disabled}
          onChange={(e) => {
            setNameText(e.target.value);
            setNameError(null);
          }}
          onBlur={commitName}
          onKeyDown={(e) => {
            if (e.key === "Enter") commitName();
            if (e.key === "Escape") {
              setNameText(null);
              setNameError(null);
            }
          }}
        />
        <span className="text-muted">guards</span>
        {users.length === 0 ? (
          <span className="text-muted">no service</span>
        ) : (
          users.map((s) => <Badge key={s}>{s}</Badge>)
        )}
        <Button
          variant="danger"
          icon="trash"
          className="ml-auto"
          disabled={disabled || users.length > 0}
          title={
            users.length > 0
              ? `In use by ${users.join(", ")}: point those services at another stack first`
              : undefined
          }
          aria-label={`Remove stack ${stack.name}`}
          onClick={() => onChange((d) => removeStack(d, stack.key))}
        >
          Remove stack
        </Button>
      </header>
      {nameError && (
        <p className="m-0 px-2 pt-1 text-[11px] text-bad" role="alert">
          {nameError}
        </p>
      )}
      <ol className="m-0 list-none space-y-1.5 p-2">
        {stack.entries.map((entry, i) => (
          <EntryRow
            key={entry.key}
            stack={stack}
            entry={entry}
            index={i}
            disabled={disabled}
            onChange={onChange}
          />
        ))}
        {stack.entries.length === 0 && (
          <li className="text-xs text-muted">
            No modules: nothing can succeed, so access is denied.
          </li>
        )}
      </ol>
      <div className="border-t border-line px-2 py-1.5">
        <Button
          icon="plus"
          disabled={disabled}
          onClick={() => onChange((d) => addEntry(d, stack.key))}
        >
          Add module
        </Button>
      </div>
    </section>
  );
}

function EntryRow({
  stack,
  entry,
  index,
  disabled,
  onChange,
}: {
  stack: DraftStack;
  entry: DraftEntry;
  index: number;
  disabled?: boolean;
  onChange: (edit: (d: Draft) => Draft) => void;
}) {
  const options = optionRows(entry);
  return (
    <li
      className="rounded-md border border-line px-2 py-1.5 text-xs"
      data-testid={`stack-entry-${stack.name}-${index}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="tabular-nums text-muted">{index + 1}.</span>
        <Select
          aria-label={`Module ${index + 1} of stack ${stack.name}`}
          className="w-44 font-mono"
          value={entry.module}
          disabled={disabled}
          onChange={(e) =>
            onChange((d) =>
              setEntryModule(d, stack.key, entry.key, e.target.value),
            )
          }
        >
          {choices(MODULES, entry.module).map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </Select>
        <Select
          aria-label={`Control of module ${index + 1} of stack ${stack.name}`}
          className="w-32"
          value={entry.control}
          disabled={disabled}
          onChange={(e) =>
            onChange((d) =>
              setEntryControl(d, stack.key, entry.key, e.target.value),
            )
          }
        >
          {choices(CONTROLS, entry.control).map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </Select>
        <span className="ml-auto flex items-center gap-1">
          <Button
            variant="ghost"
            icon="up"
            aria-label={`Move module ${index + 1} up`}
            disabled={disabled || index === 0}
            onClick={() =>
              onChange((d) => moveEntry(d, stack.key, entry.key, -1))
            }
          />
          <Button
            variant="ghost"
            icon="down"
            aria-label={`Move module ${index + 1} down`}
            disabled={disabled || index === stack.entries.length - 1}
            onClick={() =>
              onChange((d) => moveEntry(d, stack.key, entry.key, 1))
            }
          />
          <Button
            variant="ghost"
            icon="close"
            aria-label={`Remove module ${index + 1}`}
            disabled={disabled}
            onClick={() =>
              onChange((d) => removeEntry(d, stack.key, entry.key))
            }
          />
        </span>
      </div>
      {options.length > 0 && (
        <dl className="m-0 mt-1 grid grid-cols-[8rem_1fr] gap-x-3 pl-5 font-mono text-[11px]">
          {options.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted">{k}</dt>
              <dd className="m-0 break-all">{v}</dd>
            </div>
          ))}
        </dl>
      )}
    </li>
  );
}
