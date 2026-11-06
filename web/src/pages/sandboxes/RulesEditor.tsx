import type { SandboxConfig } from "../../api/sandboxes";
import { Button, Select, TextInput } from "../../components/ui";

type Rules = SandboxConfig["approval_rules"];
type Action = Rules["shell"][number]["action"];

const ACTIONS: readonly { value: Action; label: string }[] = [
  { value: "auto", label: "Run" },
  { value: "approve", label: "Ask" },
  { value: "deny", label: "Deny" },
];

/**
 * Rules of `approval: rules`: the first matching rule decides, and a call no
 * rule matches waits for approval in the Inbox.
 */
export function RulesEditor({
  rules,
  onChange,
}: {
  rules: Rules;
  onChange: (r: Rules) => void;
}) {
  return (
    <div className="space-y-4">
      <RuleList
        title="Shell commands"
        hint="Regular expression searched anywhere in the command; anchor it with ^ and $."
        placeholder="^(ls|cat|git status)\b"
        addLabel="Add command rule"
        rows={rules.shell.map((r) => ({ pattern: r.match, action: r.action }))}
        onChange={(rows) =>
          onChange({
            ...rules,
            shell: rows.map((r) => ({ match: r.pattern, action: r.action })),
          })
        }
      />
      <RuleList
        title="File writes"
        hint="Glob relative to the workdir: * within a folder, ** across folders."
        placeholder="notes/**"
        addLabel="Add path rule"
        rows={rules.fs_write.map((r) => ({
          pattern: r.path,
          action: r.action,
        }))}
        onChange={(rows) =>
          onChange({
            ...rules,
            fs_write: rows.map((r) => ({ path: r.pattern, action: r.action })),
          })
        }
      />
    </div>
  );
}

interface Row {
  pattern: string;
  action: Action;
}

function RuleList({
  title,
  hint,
  placeholder,
  addLabel,
  rows,
  onChange,
}: {
  title: string;
  hint: string;
  placeholder: string;
  addLabel: string;
  rows: Row[];
  onChange: (rows: Row[]) => void;
}) {
  const set = (i: number, patch: Partial<Row>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="space-y-1.5" data-rules={title}>
      <div className="text-[11px] font-medium text-muted">{title}</div>
      <div className="text-[11px] text-muted">{hint}</div>
      {rows.map((r, i) => (
        <div key={i} className="flex items-center gap-1.5">
          <span className="w-4 text-right text-[11px] tabular-nums text-muted">
            {i + 1}
          </span>
          <TextInput
            aria-label={`${title} rule ${i + 1} pattern`}
            className="font-mono"
            value={r.pattern}
            placeholder={placeholder}
            onChange={(e) => set(i, { pattern: e.target.value })}
          />
          <Select
            aria-label={`${title} rule ${i + 1} action`}
            className="!w-24"
            value={r.action}
            onChange={(e) => set(i, { action: e.target.value as Action })}
          >
            {ACTIONS.map((a) => (
              <option key={a.value} value={a.value}>
                {a.label}
              </option>
            ))}
          </Select>
          <Button
            icon="trash"
            aria-label={`Remove ${title} rule ${i + 1}`}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          />
        </div>
      ))}
      <Button
        icon="plus"
        onClick={() => onChange([...rows, { pattern: "", action: "approve" }])}
      >
        {addLabel}
      </Button>
    </div>
  );
}
