import { useMemo, useState } from "react";
import { errorMessage } from "../api/client";
import { useCreatePrompt } from "../api/prompts";
import { buildPromptMarkdown } from "../pages/prompts/skeleton";
import { CodeEditor } from "../components/CodeEditor";
import { Icon } from "../components/icons";
import {
  Button,
  Field,
  Select,
  TextArea,
  TextInput,
  Toggle,
} from "../components/ui";
import { langForCommand } from "../lib/codeLang";
import { cx } from "../lib/cx";
import {
  AGENT_MODES,
  APPROVE_STYLES,
  LANGS,
  PRIORITIES,
  type AgentCwd,
  type AgentNode,
  type CommandNode,
  type Diagnostic,
  type FlowNode,
  type InboxNode,
  type Lang,
} from "./model";
import {
  promptSource,
  templateHints,
  toInlinePrompt,
  toSavedPrompt,
  type PromptSource,
} from "./agentPrompt";
import { discoverFields } from "./transform/fields";
import { useSampleInput } from "./transform/useSampleInput";
import { TransformForm } from "./transform/TransformForm";
import { diagnosticField } from "./validate";
import { ToolsSection } from "./ToolsSection";

export interface InspectorProps {
  node: FlowNode;
  diagnostics: Diagnostic[];
  prompts: string[];
  runtimes: string[];
  /** Whether another node already uses this id. */
  idTaken: (id: string) => boolean;
  /** Id of the flow being edited (samples are looked up per flow and node). */
  flowId: string;
  /** `coalesce` merges consecutive edits of one field into one undo step. */
  onChange: (patch: (n: FlowNode) => FlowNode, coalesce?: string) => void;
  onRename: (id: string) => void;
  onDelete: () => void;
}

/** Messages of diagnostics that point at `field` (e.g. "script", "ports"). */
function msgs(diags: Diagnostic[], field: string): string[] {
  return diags
    .filter((d) => diagnosticField(d) === field)
    .map((d) => d.message);
}

/** Set `key` on `obj`, removing it when the value is empty. */
function setOpt<T extends object>(obj: T, key: string, value: unknown): T {
  const next = { ...obj } as Record<string, unknown>;
  if (
    value === "" ||
    value === undefined ||
    (Array.isArray(value) && value.length === 0)
  ) {
    delete next[key];
  } else {
    next[key] = value;
  }
  return next as T;
}

/** Text area holding one list item per line; commits on blur. */
function LinesInput({
  value,
  onCommit,
  placeholder,
  invalid,
  rows = 3,
}: {
  value: string[];
  onCommit: (v: string[]) => void;
  placeholder?: string;
  invalid?: boolean;
  rows?: number;
}) {
  const [draft, setDraft] = useState(value.join("\n"));
  return (
    <TextArea
      rows={rows}
      value={draft}
      placeholder={placeholder}
      invalid={invalid}
      spellCheck={false}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() =>
        onCommit(
          draft
            .split("\n")
            .map((l) => l.trim())
            .filter((l) => l !== ""),
        )
      }
    />
  );
}

/** Comma separated list input; commits on blur. */
function ListInput({
  value,
  onCommit,
  placeholder,
  invalid,
}: {
  value: string[];
  onCommit: (v: string[]) => void;
  placeholder?: string;
  invalid?: boolean;
}) {
  const [draft, setDraft] = useState(value.join(", "));
  return (
    <TextInput
      value={draft}
      placeholder={placeholder}
      invalid={invalid}
      spellCheck={false}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() =>
        onCommit(
          draft
            .split(",")
            .map((p) => p.trim())
            .filter((p) => p !== ""),
        )
      }
    />
  );
}

function CommandForm({
  node,
  diags,
  onChange,
}: {
  node: CommandNode;
  diags: Diagnostic[];
  onChange: (p: (n: CommandNode) => CommandNode) => void;
}) {
  return (
    <>
      <Field label="Language">
        <Select
          value={node.lang}
          onChange={(e) =>
            onChange((n) => ({ ...n, lang: e.target.value as Lang }))
          }
        >
          {LANGS.map((l) => (
            <option key={l}>{l}</option>
          ))}
        </Select>
      </Field>
      {node.lang === "exec" ? (
        <Field
          label="Command (one argument per line)"
          error={msgs(diags, "command")}
          hint="Run directly, without a shell."
        >
          <LinesInput
            value={node.command ?? []}
            invalid={msgs(diags, "command").length > 0}
            placeholder={"jq\n-c\n.items[]"}
            onCommit={(v) => onChange((n) => setOpt(n, "command", v))}
          />
        </Field>
      ) : (
        <Field label="Script" error={msgs(diags, "script")}>
          <CodeEditor
            label="Script"
            lang={langForCommand(node.lang)}
            value={node.script ?? ""}
            invalid={msgs(diags, "script").length > 0}
            onChange={(v) => onChange((n) => ({ ...n, script: v }))}
            minHeight="9rem"
          />
        </Field>
      )}
      <Field
        label="Output ports"
        error={msgs(diags, "ports")}
        hint='Comma separated. Empty means a single "out" port; "error" is always present.'
      >
        <ListInput
          value={node.ports ?? []}
          invalid={msgs(diags, "ports").length > 0}
          placeholder="out"
          onCommit={(v) => onChange((n) => setOpt(n, "ports", v))}
        />
      </Field>
      <div className="grid grid-cols-2 gap-2">
        <Field
          label="Timeout"
          error={msgs(diags, "timeout")}
          hint="Default 60s"
        >
          <TextInput
            value={node.timeout ?? ""}
            placeholder="60s"
            onChange={(e) =>
              onChange((n) => setOpt(n, "timeout", e.target.value))
            }
          />
        </Field>
        <Field label="Working directory" error={msgs(diags, "cwd")}>
          <TextInput
            value={node.cwd?.path ?? ""}
            placeholder="$HAL_STATE_DIR"
            onChange={(e) =>
              onChange((n) =>
                setOpt(
                  n,
                  "cwd",
                  e.target.value === ""
                    ? undefined
                    : { mode: "dir", path: e.target.value },
                ),
              )
            }
          />
        </Field>
      </div>
      <Toggle
        label="Side effect (skipped in dry-run)"
        checked={node.side_effect === true}
        onChange={(v) =>
          onChange((n) => {
            const next = { ...n };
            if (v) next.side_effect = true;
            else delete next.side_effect;
            return next;
          })
        }
      />
    </>
  );
}

function CwdForm({
  cwd,
  diags,
  onChange,
}: {
  cwd: AgentCwd | undefined;
  diags: Diagnostic[];
  onChange: (c: AgentCwd | undefined) => void;
}) {
  const mode = cwd?.mode ?? "default";
  const errs = (f: string) => msgs(diags, f);
  return (
    <div className="space-y-2 rounded-md border border-line p-2">
      <Field label="Working directory">
        <Select
          value={mode}
          onChange={(e) => {
            const m = e.target.value;
            if (m === "dir") onChange({ mode: "dir", path: "" });
            else if (m === "worktree")
              onChange({ mode: "worktree", repo: "", ref: "origin/main" });
            else onChange(undefined);
          }}
        >
          <option value="default">default (per-node state dir)</option>
          <option value="dir">existing directory</option>
          <option value="worktree">fresh git worktree</option>
        </Select>
      </Field>
      {cwd?.mode === "dir" && (
        <Field label="Path (template)" error={errs("path")}>
          <TextInput
            value={cwd.path ?? ""}
            onChange={(e) => onChange({ mode: "dir", path: e.target.value })}
          />
        </Field>
      )}
      {cwd?.mode === "worktree" && (
        <>
          <Field label="Repo (template)" error={errs("repo")}>
            <TextInput
              value={cwd.repo ?? ""}
              placeholder="~/code/{{.input.repository.name}}"
              onChange={(e) => onChange({ ...cwd, repo: e.target.value })}
            />
          </Field>
          <Field label="Ref" error={errs("ref")}>
            <TextInput
              value={cwd.ref ?? ""}
              placeholder="origin/main"
              onChange={(e) => onChange({ ...cwd, ref: e.target.value })}
            />
          </Field>
        </>
      )}
    </div>
  );
}

function PromptSection({
  node,
  diags,
  prompts,
  flowId,
  onChange,
}: {
  node: AgentNode;
  diags: Diagnostic[];
  prompts: string[];
  flowId: string;
  onChange: (p: (n: AgentNode) => AgentNode) => void;
}) {
  const [source, setSource] = useState<PromptSource>(promptSource(node));
  const [saveName, setSaveName] = useState("");
  const create = useCreatePrompt();
  const sample = useSampleInput(flowId, node.id);
  const hints = useMemo(
    () =>
      sample.input === undefined
        ? []
        : templateHints(discoverFields(sample.input).map((f) => f.path)),
    [sample.input],
  );
  const inline = source === "inline";
  const instructionErrors = msgs(diags, "instructions");
  const nameOk =
    /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(saveName) &&
    !prompts.includes(saveName);
  const save = () =>
    create.mutate(
      buildPromptMarkdown({
        name: saveName,
        description: "",
        output: node.output ?? "note",
        body: node.instructions ?? "",
      }),
      {
        onSuccess: (detail) => {
          onChange((n) => toSavedPrompt(n, detail.name));
          setSource("library");
          setSaveName("");
        },
      },
    );
  return (
    <div className="space-y-2" data-section="prompt">
      <div className="flex items-center justify-between gap-2">
        <h3 className="m-0 text-xs font-semibold">Prompt</h3>
        <Toggle
          label="Use a saved prompt"
          checked={!inline}
          onChange={(on) => {
            setSource(on ? "library" : "inline");
            onChange((n) => (on ? toSavedPrompt(n) : toInlinePrompt(n)));
          }}
        />
      </div>
      {inline ? (
        <>
          <Field
            label="Instructions (template)"
            error={instructionErrors}
            hint={
              hints.length > 0
                ? `Variables from the sample: ${hints.join(" ")}`
                : "Go template: {{.input.field}} is the item, {{.event}} the event."
            }
          >
            <CodeEditor
              label="Instructions"
              lang="markdown"
              value={node.instructions ?? ""}
              invalid={instructionErrors.length > 0}
              onChange={(v) => onChange((n) => ({ ...n, instructions: v }))}
              minHeight="8rem"
              maxHeight="20rem"
            />
          </Field>
          <Field
            label="Output"
            error={msgs(diags, "output")}
            hint="note: the agent ends with an inbox note; none: free text."
          >
            <Select
              value={node.output ?? "note"}
              onChange={(e) =>
                onChange((n) => ({
                  ...n,
                  output: e.target.value as "note" | "none",
                }))
              }
            >
              <option>note</option>
              <option>none</option>
            </Select>
          </Field>
          <div className="flex items-end gap-2">
            <Field
              label="Save as reusable prompt"
              error={create.error ? [errorMessage(create.error)] : []}
            >
              <TextInput
                value={saveName}
                placeholder="prompt name"
                onChange={(e) => setSaveName(e.target.value)}
              />
            </Field>
            <Button
              disabled={
                !nameOk ||
                (node.instructions ?? "").trim() === "" ||
                create.isPending
              }
              onClick={save}
            >
              Save
            </Button>
          </div>
        </>
      ) : (
        <Field
          label="Saved prompt"
          error={[...msgs(diags, "prompt"), ...instructionErrors]}
          hint="From the prompt library."
        >
          <Select
            value={node.prompt ?? ""}
            invalid={msgs(diags, "prompt").length > 0}
            onChange={(e) =>
              onChange((n) => ({ ...n, prompt: e.target.value }))
            }
          >
            <option value="">(choose a prompt)</option>
            {prompts.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </Select>
        </Field>
      )}
    </div>
  );
}

function AgentForm({
  node,
  diags,
  prompts,
  runtimes,
  flowId,
  onChange,
}: {
  flowId: string;
  node: AgentNode;
  diags: Diagnostic[];
  prompts: string[];
  runtimes: string[];
  onChange: (p: (n: AgentNode) => AgentNode) => void;
}) {
  const promptOptions =
    node.prompt && !prompts.includes(node.prompt)
      ? [node.prompt, ...prompts]
      : prompts;
  const runtimeList = `runtimes-${node.id}`;
  return (
    <>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Runtime" error={msgs(diags, "runtime")}>
          <TextInput
            list={runtimeList}
            value={node.runtime}
            invalid={msgs(diags, "runtime").length > 0}
            onChange={(e) =>
              onChange((n) => ({ ...n, runtime: e.target.value }))
            }
          />
          <datalist id={runtimeList}>
            {runtimes.map((r) => (
              <option key={r} value={r} />
            ))}
          </datalist>
        </Field>
        <Field label="Model" error={msgs(diags, "model")}>
          <TextInput
            value={node.model ?? ""}
            placeholder="runtime default"
            onChange={(e) =>
              onChange((n) => setOpt(n, "model", e.target.value))
            }
          />
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Mode" error={msgs(diags, "mode")}>
          <Select
            value={node.mode}
            onChange={(e) =>
              onChange((n) => {
                const mode = e.target.value as AgentNode["mode"];
                const next = { ...n, mode };
                if (mode !== "approve") {
                  delete next.approve;
                  delete next.approval_timeout;
                }
                return next;
              })
            }
          >
            {AGENT_MODES.map((m) => (
              <option key={m}>{m}</option>
            ))}
          </Select>
        </Field>
        {node.mode === "approve" && (
          <Field label="Approve style" error={msgs(diags, "approve")}>
            <Select
              value={node.approve ?? "proposal"}
              onChange={(e) =>
                onChange((n) => ({
                  ...n,
                  approve: e.target.value as AgentNode["approve"],
                }))
              }
            >
              {APPROVE_STYLES.map((m) => (
                <option key={m}>{m}</option>
              ))}
            </Select>
          </Field>
        )}
      </div>
      <PromptSection
        node={node}
        diags={diags}
        prompts={promptOptions}
        flowId={flowId}
        onChange={onChange}
      />
      <Field
        label="Extra prompt (template)"
        error={msgs(diags, "extra_prompt")}
      >
        <TextArea
          rows={3}
          value={node.extra_prompt ?? ""}
          onChange={(e) =>
            onChange((n) => setOpt(n, "extra_prompt", e.target.value))
          }
        />
      </Field>
      <CwdForm
        cwd={node.cwd}
        diags={diags}
        onChange={(c) => onChange((n) => setOpt(n, "cwd", c))}
      />
      <Field
        label="Allow: tools (regex, one per line)"
        error={msgs(diags, "tools")}
        hint="Tools of this node's MCP servers the agent may call; matched on tool title/name. hal's own tools are always allowed."
      >
        <LinesInput
          value={node.allow?.tools ?? []}
          onCommit={(v) =>
            onChange((n) =>
              setOpt(n, "allow", compactAllow({ ...n.allow, tools: v })),
            )
          }
        />
      </Field>
      <ToolsSection
        value={node.tools}
        error={msgs(diags, "tools")}
        onChange={(v) => onChange((n) => setOpt(n, "tools", v))}
      />
      <div className="grid grid-cols-2 gap-2">
        <Field
          label="Timeout"
          error={msgs(diags, "timeout")}
          hint="Default 30m"
        >
          <TextInput
            value={node.timeout ?? ""}
            placeholder="30m"
            onChange={(e) =>
              onChange((n) => setOpt(n, "timeout", e.target.value))
            }
          />
        </Field>
        <Field
          label="Approval timeout"
          error={msgs(diags, "approval_timeout")}
          hint="Default 1h"
        >
          <TextInput
            value={node.approval_timeout ?? ""}
            placeholder="1h"
            onChange={(e) =>
              onChange((n) => setOpt(n, "approval_timeout", e.target.value))
            }
          />
        </Field>
      </div>
    </>
  );
}

function compactAllow(a: { tools?: string[] }) {
  const out: { tools?: string[] } = {};
  if (a.tools && a.tools.length > 0) out.tools = a.tools;
  return Object.keys(out).length > 0 ? out : undefined;
}

function InboxForm({
  node,
  diags,
  onChange,
}: {
  node: InboxNode;
  diags: Diagnostic[];
  onChange: (p: (n: InboxNode) => InboxNode) => void;
}) {
  return (
    <>
      <Field label="Title (template)" error={msgs(diags, "title")}>
        <TextInput
          value={node.title}
          invalid={msgs(diags, "title").length > 0}
          onChange={(e) => onChange((n) => ({ ...n, title: e.target.value }))}
        />
      </Field>
      <Field label="Priority" error={msgs(diags, "priority")}>
        <Select
          value={node.priority ?? "normal"}
          onChange={(e) =>
            onChange((n) => ({
              ...n,
              priority: e.target.value as InboxNode["priority"],
            }))
          }
        >
          {PRIORITIES.map((p) => (
            <option key={p}>{p}</option>
          ))}
        </Select>
      </Field>
      <Field
        label="Summary (markdown template)"
        error={msgs(diags, "summary_md")}
      >
        <CodeEditor
          label="Summary"
          lang="markdown"
          value={node.summary_md ?? ""}
          onChange={(v) => onChange((n) => setOpt(n, "summary_md", v))}
          minHeight="5rem"
          maxHeight="14rem"
        />
      </Field>
      <Field
        label="Suggested reply (template)"
        error={msgs(diags, "suggested_reply")}
      >
        <TextArea
          rows={3}
          value={node.suggested_reply ?? ""}
          onChange={(e) =>
            onChange((n) => setOpt(n, "suggested_reply", e.target.value))
          }
        />
      </Field>
    </>
  );
}

export function Inspector(props: InspectorProps) {
  const { node, diagnostics } = props;
  const [draftId, setDraftId] = useState(node.id);
  const idError =
    draftId !== node.id &&
    (draftId.trim() === "" || props.idTaken(draftId.trim()));
  const nodeLevel = diagnostics.filter(
    (d) => diagnosticField(d) === undefined || diagnosticField(d) === "edges",
  );

  const commitId = () => {
    if (draftId.trim() === node.id) return;
    if (idError) {
      setDraftId(node.id);
      return;
    }
    props.onRename(draftId.trim());
  };

  return (
    <div className="space-y-3 p-3" data-testid="inspector">
      <div className="flex items-center gap-2">
        <span className="rounded bg-raised px-1.5 py-0.5 text-[11px] font-medium uppercase tracking-wide text-muted">
          {node.type}
        </span>
        <button
          type="button"
          onClick={props.onDelete}
          className="ml-auto inline-flex items-center gap-1 rounded px-1.5 py-1 text-[11px] text-muted hover:bg-bad/10 hover:text-bad"
          title="Delete node (Backspace)"
        >
          <Icon name="trash" size={13} /> Delete
        </button>
      </div>

      <Field
        label="Node id"
        error={
          idError
            ? [
                draftId.trim() === ""
                  ? "Id cannot be empty"
                  : `"${draftId.trim()}" is already used`,
              ]
            : msgs(diagnostics, "id")
        }
      >
        <TextInput
          value={draftId}
          invalid={idError || msgs(diagnostics, "id").length > 0}
          onChange={(e) => setDraftId(e.target.value)}
          onBlur={commitId}
          onKeyDown={(e) => {
            if (e.key === "Enter") e.currentTarget.blur();
            if (e.key === "Escape") setDraftId(node.id);
          }}
        />
      </Field>

      {nodeLevel.length > 0 && (
        <ul className="space-y-1" aria-label="Node diagnostics">
          {nodeLevel.map((d) => (
            <li
              key={d.message}
              className={cx(
                "flex gap-1.5 text-[11px]",
                d.severity === "error" ? "text-bad" : "text-warn",
              )}
            >
              <Icon
                name={d.severity === "error" ? "error" : "warning"}
                size={13}
                className="mt-px shrink-0"
              />
              {d.message}
            </li>
          ))}
        </ul>
      )}

      {node.type === "command" && (
        <CommandForm
          node={node}
          diags={diagnostics}
          onChange={(p) =>
            props.onChange((n) => (n.type === "command" ? p(n) : n))
          }
        />
      )}
      {node.type === "agent" && (
        <AgentForm
          node={node}
          diags={diagnostics}
          prompts={props.prompts}
          runtimes={props.runtimes}
          flowId={props.flowId}
          onChange={(p) =>
            props.onChange((n) => (n.type === "agent" ? p(n) : n))
          }
        />
      )}
      {node.type === "inbox" && (
        <InboxForm
          node={node}
          diags={diagnostics}
          onChange={(p) =>
            props.onChange((n) => (n.type === "inbox" ? p(n) : n))
          }
        />
      )}
      {node.type === "transform" && (
        <TransformForm
          node={node}
          flowId={props.flowId}
          diags={diagnostics}
          onChange={props.onChange}
        />
      )}
    </div>
  );
}
