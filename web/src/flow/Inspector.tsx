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
import { runtimeOptions, type RuntimeOption } from "../lib/runtimeStatus";
import {
  AGENT_MODES,
  APPROVE_STYLES,
  LANGS,
  PRIORITIES,
  type AgentCwd,
  ON_TIMEOUTS,
  type AgentNode,
  type ApprovalNode,
  type CommandNode,
  type Diagnostic,
  type Flow,
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
import {
  addPort,
  freshPortName,
  portNameError,
  setPort,
  unusedPorts,
  unusedPortMessage,
} from "./agentPorts";
import { outputsSection } from "./outputsPreview";
import { parsePortSchema, schemaText } from "./portSchema";
import { discoverFields } from "./transform/fields";
import { useSampleInput } from "./transform/useSampleInput";
import { TransformForm } from "./transform/TransformForm";
import { diagnosticField } from "./validate";
import { SandboxSection } from "./SandboxSection";
import { ToolsSection } from "./ToolsSection";
import { RequestBody } from "../pages/approvals/RequestBody";
import { EditFieldsPreview } from "../pages/approvals/EditFields";
import { PriorityBadge } from "../pages/inbox/InboxList";
import { fieldChoices, previewApproval, toggleField } from "./approval";

export interface InspectorProps {
  node: FlowNode;
  diagnostics: Diagnostic[];
  prompts: string[];
  runtimes: RuntimeOption[];
  /** Whether another node already uses this id. */
  idTaken: (id: string) => boolean;
  /** Id of the flow being edited (samples are looked up per flow and node). */
  flowId: string;
  /** The flow being edited (current graph), for the generated outputs preview. */
  flow: Flow;
  /** `coalesce` merges consecutive edits of one field into one undo step. */
  onChange: (patch: (n: FlowNode) => FlowNode, coalesce?: string) => void;
  onRename: (id: string) => void;
  /** Rename an output port of this agent node; its edges follow. */
  onRenamePort: (from: string, to: string) => void;
  /** Remove an output port of this agent node and its edges. */
  onRemovePort: (port: string) => void;
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

/** JSON editor for a port's schema; keeps the raw text so invalid input is not lost while typing. */
function PortSchemaField({
  schema,
  errors: external,
  onChange,
}: {
  schema: Record<string, unknown> | undefined;
  errors: string[];
  onChange: (s: Record<string, unknown> | undefined) => void;
}) {
  const key = JSON.stringify(schema ?? null);
  const [text, setText] = useState(schemaText(schema));
  const [seen, setSeen] = useState(key);
  const [parseError, setParseError] = useState<string>();
  if (seen !== key) {
    // The node changed outside this editor (undo, assist): show it.
    setSeen(key);
    setText(schemaText(schema));
    setParseError(undefined);
  }
  const apply = (next: string) => {
    setText(next);
    const r = parsePortSchema(next);
    if (!r.ok) {
      setParseError(r.error);
      return;
    }
    setParseError(undefined);
    setSeen(JSON.stringify(r.value ?? null));
    onChange(r.value);
  };
  const errors = [...(parseError ? [parseError] : []), ...external];
  return (
    <div className="space-y-1">
      <Field
        label="Schema (JSON Schema)"
        error={errors}
        hint="Arguments of the emit tool; the top level must be type: object. Empty uses the inbox note schema, valid only when the port leads directly to inbox nodes."
      >
        <CodeEditor
          label="Port schema"
          lang="json"
          value={text}
          invalid={errors.length > 0}
          onChange={apply}
          minHeight="6rem"
          maxHeight="16rem"
        />
      </Field>
      <Button disabled={text === ""} onClick={() => apply("")}>
        Clear (inbox shortcut)
      </Button>
    </div>
  );
}

/** Port name field: edits locally, renames (with its edges) on commit when valid. */
function PortNameInput({
  name,
  others,
  onRename,
}: {
  name: string;
  others: string[];
  onRename: (to: string) => void;
}) {
  const [draft, setDraft] = useState(name);
  const error = draft === name ? undefined : portNameError(draft, others);
  const commit = () => {
    if (draft === name) return;
    if (error) setDraft(name);
    else onRename(draft);
  };
  return (
    <Field label="Name" error={error ? [error] : []}>
      <TextInput
        value={draft}
        invalid={error !== undefined}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.currentTarget.blur();
          if (e.key === "Escape") setDraft(name);
        }}
      />
    </Field>
  );
}

function portDiagMessages(diags: Diagnostic[], port: string): string[] {
  return diags
    .filter((d) => {
      const m = /\.ports\.([^.[]+)/.exec(d.path ?? "");
      return m !== null && m[1] === port && d.code !== "unused_port";
    })
    .map((d) => d.message);
}

function PortsSection({
  node,
  flow,
  diags,
  onChange,
  onRenamePort,
  onRemovePort,
}: {
  node: AgentNode;
  flow: Flow;
  diags: Diagnostic[];
  onChange: (p: (n: AgentNode) => AgentNode) => void;
  onRenamePort: (from: string, to: string) => void;
  onRemovePort: (port: string) => void;
}) {
  const names = Object.keys(node.ports ?? {});
  const unused = unusedPorts(node, flow.edges);
  return (
    <div className="space-y-2" data-section="ports">
      <div className="flex items-center gap-2">
        <span className="text-[11px] font-medium text-muted">Output ports</span>
        <Button
          onClick={() => onChange((n) => addPort(n, freshPortName(names)))}
        >
          Add port
        </Button>
      </div>
      {names.length === 0 && (
        <div className="text-[11px] text-muted">
          Without ports the agent ends with a note (see Output). With ports it
          must finish by calling one emit tool per outcome, and each port is a
          handle on the node.
        </div>
      )}
      {msgs(diags, "ports").map((m) => (
        <div key={m} className="text-[11px] text-bad">
          {m}
        </div>
      ))}
      {names.map((name) => (
        <div
          key={name}
          className="space-y-2 rounded border border-line p-2"
          data-port={name}
        >
          <div className="flex items-end gap-2">
            <div className="flex-1">
              <PortNameInput
                name={name}
                others={names.filter((n) => n !== name)}
                onRename={(to) => onRenamePort(name, to)}
              />
            </div>
            <Button onClick={() => onRemovePort(name)}>Remove</Button>
          </div>
          {unused.includes(name) && (
            <div className="flex gap-1.5 text-[11px] text-warn">
              <Icon name="warning" size={13} className="mt-px shrink-0" />
              {unusedPortMessage(name)}
            </div>
          )}
          <Field
            label="Description"
            hint="Shown to the agent as the tool description."
          >
            <TextInput
              value={node.ports?.[name]?.description ?? ""}
              onChange={(e) =>
                onChange((n) =>
                  setPort(n, name, { description: e.target.value }),
                )
              }
            />
          </Field>
          <PortSchemaField
            schema={node.ports?.[name]?.schema}
            errors={portDiagMessages(diags, name)}
            onChange={(schema) => onChange((n) => setPort(n, name, { schema }))}
          />
        </div>
      ))}
      {names.length > 0 && (
        <Field
          label="Emit"
          hint="one: the last valid call wins. many: each call is a separate emission; downstream runs once per emission."
        >
          <Select
            value={node.emit ?? "one"}
            onChange={(e) =>
              onChange((n) => {
                const { emit: _e, ...rest } = n;
                return e.target.value === "many"
                  ? { ...rest, emit: "many" }
                  : rest;
              })
            }
          >
            <option value="one">one</option>
            <option value="many">many</option>
          </Select>
        </Field>
      )}
      <OutputsPreview node={node} flow={flow} />
    </div>
  );
}

function OutputsPreview({ node, flow }: { node: AgentNode; flow: Flow }) {
  const text = outputsSection(flow, node);
  if (text === undefined) return null;
  return (
    <div className="space-y-1" data-section="outputs-preview">
      <div className="text-[11px] font-medium text-muted">
        Generated outputs section
      </div>
      <pre className="overflow-x-auto whitespace-pre-wrap rounded border border-line bg-raised p-2 font-mono text-[11px]">
        {text}
      </pre>
      <div className="text-[11px] text-muted">
        hal appends this to the prompt at run time; it is not saved in the flow
        file.
      </div>
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
  const hasPorts = Object.keys(node.ports ?? {}).length > 0;
  const instructionErrors = msgs(diags, "instructions");
  const nameOk =
    /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(saveName) &&
    !prompts.includes(saveName);
  const save = () =>
    create.mutate(
      buildPromptMarkdown({
        name: saveName,
        description: "",
        output: node.output === "none" ? "none" : "note",
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
          {hasPorts ? (
            <div className="text-[11px] text-muted" data-section="output">
              Output: the agent returns its result through the emit tools of its
              ports.
            </div>
          ) : (
            <Field
              label="Output"
              error={msgs(diags, "output")}
              hint="note: the agent ends with an inbox note (also via submit_output); none: free text."
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
                <option value="note">note</option>
                <option value="none">none</option>
              </Select>
            </Field>
          )}
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
  flow,
  onChange,
  onRenamePort,
  onRemovePort,
}: {
  flowId: string;
  flow: Flow;
  node: AgentNode;
  diags: Diagnostic[];
  prompts: string[];
  runtimes: RuntimeOption[];
  onChange: (p: (n: AgentNode) => AgentNode) => void;
  onRenamePort: (from: string, to: string) => void;
  onRemovePort: (port: string) => void;
}) {
  const promptOptions =
    node.prompt && !prompts.includes(node.prompt)
      ? [node.prompt, ...prompts]
      : prompts;
  const runtimeChoices = runtimeOptions(runtimes, node.runtime);
  return (
    <>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Runtime" error={msgs(diags, "runtime")}>
          <Select
            aria-label="Runtime"
            value={node.runtime}
            invalid={msgs(diags, "runtime").length > 0}
            onChange={(e) =>
              onChange((n) => ({ ...n, runtime: e.target.value }))
            }
          >
            {node.runtime === "" && (
              <option value="">(choose a runtime)</option>
            )}
            {runtimeChoices.map((r) => (
              <option key={r.name} value={r.name}>
                {r.label}
              </option>
            ))}
          </Select>
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
                  // With requires_approval the timeout also bounds the gate.
                  if (!next.requires_approval) delete next.approval_timeout;
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
      <PortsSection
        node={node}
        flow={flow}
        diags={diags}
        onChange={onChange}
        onRenamePort={onRenamePort}
        onRemovePort={onRemovePort}
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
      <SandboxSection
        sandbox={node.sandbox}
        image={node.image}
        repo={node.repo}
        gitRef={node.ref}
        errors={{
          sandbox: msgs(diags, "sandbox"),
          image: msgs(diags, "image"),
          repo: msgs(diags, "repo"),
          ref: msgs(diags, "ref"),
        }}
        onChange={(field, v) =>
          onChange((n) => {
            let next = setOpt(n, field, v);
            if (field === "sandbox") {
              next = setOpt(next, "image", "");
              if (v === "") {
                next = setOpt(setOpt(next, "repo", ""), "ref", "");
              }
            }
            return next;
          })
        }
      />
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
          hint={
            node.requires_approval
              ? "For the approval before the node runs and for its own approvals. Empty: the gate waits until decided, own approvals default 1h."
              : "Default 1h"
          }
        >
          <TextInput
            value={node.approval_timeout ?? ""}
            placeholder={node.requires_approval ? "none" : "1h"}
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

/**
 * The "Requires approval" switch shared by every runnable node. Agents keep
 * their one Approval timeout field (it bounds the gate too); the other types
 * get a timeout field here while the switch is on.
 */
function RequiresApprovalSection({
  node,
  diags,
  onChange,
}: {
  node: Exclude<FlowNode, ApprovalNode>;
  diags: Diagnostic[];
  onChange: (p: (n: FlowNode) => FlowNode) => void;
}) {
  const on = node.requires_approval === true;
  return (
    <div className="space-y-1.5" data-section="requires-approval">
      <Toggle
        label="Requires approval"
        checked={on}
        onChange={(v) =>
          onChange((n) => {
            if (n.type === "approval") return n;
            const next = { ...n };
            if (v) {
              next.requires_approval = true;
            } else {
              delete next.requires_approval;
              if (next.type !== "agent") delete next.approval_timeout;
            }
            return next;
          })
        }
      />
      <p className="m-0 text-[11px] text-muted">
        Ask in the Inbox before this node runs. Denying sends the input down the{" "}
        <code className="font-mono">denied</code> port instead of running it.
      </p>
      {msgs(diags, "requires_approval").map((m) => (
        <p key={m} className="m-0 text-[11px] text-bad">
          {m}
        </p>
      ))}
      {on && node.type !== "agent" && (
        <Field
          label="Approval timeout"
          error={msgs(diags, "approval_timeout")}
          hint="Empty waits until someone decides; on timeout the node is denied, e.g. 24h."
        >
          <TextInput
            value={node.approval_timeout ?? ""}
            placeholder="none"
            onChange={(e) =>
              onChange((n) => setOpt(n, "approval_timeout", e.target.value))
            }
          />
        </Field>
      )}
    </div>
  );
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
      <Field
        label="Title (template)"
        error={msgs(diags, "title")}
        hint="May be empty when only agent ports without a schema lead here: the agent's title is used."
      >
        <TextInput
          value={node.title}
          invalid={msgs(diags, "title").length > 0}
          onChange={(e) => onChange((n) => ({ ...n, title: e.target.value }))}
        />
      </Field>
      <Field
        label="Priority"
        error={msgs(diags, "priority")}
        hint="A priority, or a template that renders to one."
      >
        <TextInput
          list={`priorities-${node.id}`}
          value={node.priority ?? "normal"}
          onChange={(e) =>
            onChange((n) => ({ ...n, priority: e.target.value }))
          }
        />
        <datalist id={`priorities-${node.id}`}>
          {PRIORITIES.map((p) => (
            <option key={p} value={p} />
          ))}
        </datalist>
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

function ApprovalForm({
  node,
  flowId,
  diags,
  onChange,
}: {
  node: ApprovalNode;
  flowId: string;
  diags: Diagnostic[];
  onChange: (p: (n: ApprovalNode) => ApprovalNode) => void;
}) {
  const sample = useSampleInput(flowId, node.id);
  const paths = useMemo(() => fieldChoices(sample.input), [sample.input]);
  const hints = useMemo(() => templateHints(paths), [paths]);
  const fields = node.fields ?? [];
  const editable = node.editable ?? [];
  const offered = paths.filter((p) => !fields.includes(p));
  const hint =
    hints.length > 0
      ? `Variables from the sample: ${hints.join(" ")}`
      : "Go template: {{.input.field}} is the item, {{.event}} the event.";
  const preview =
    sample.input === undefined
      ? undefined
      : previewApproval(node, sample.input);
  return (
    <>
      <Field label="Title (template)" error={msgs(diags, "title")} hint={hint}>
        <TextInput
          value={node.title}
          invalid={msgs(diags, "title").length > 0}
          onChange={(e) => onChange((n) => ({ ...n, title: e.target.value }))}
        />
      </Field>
      <Field
        label="Description (template, required)"
        error={msgs(diags, "description")}
        hint="One or two sentences on what the approver is asked and why."
      >
        <TextArea
          rows={3}
          value={node.description ?? ""}
          invalid={msgs(diags, "description").length > 0}
          onChange={(e) =>
            onChange((n) => ({ ...n, description: e.target.value }))
          }
        />
      </Field>
      <Field label="Body (markdown template)" error={msgs(diags, "body")}>
        <CodeEditor
          label="Body"
          lang="markdown"
          value={node.body ?? ""}
          onChange={(v) => onChange((n) => setOpt(n, "body", v))}
          minHeight="5rem"
          maxHeight="14rem"
        />
      </Field>
      <Field
        label="Fields"
        error={msgs(diags, "fields")}
        hint="Input values shown to the approver as a small table."
      >
        <div className="space-y-1.5">
          {fields.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {fields.map((f) => (
                <button
                  key={f}
                  type="button"
                  title="Remove this field"
                  className="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-[11px] hover:border-bad"
                  onClick={() =>
                    onChange((n) => setOpt(n, "fields", toggleField(fields, f)))
                  }
                >
                  {f} ×
                </button>
              ))}
            </div>
          )}
          <Select
            aria-label="Add a field"
            value=""
            onChange={(e) =>
              e.target.value &&
              onChange((n) =>
                setOpt(n, "fields", toggleField(fields, e.target.value)),
              )
            }
          >
            <option value="">
              {paths.length === 0
                ? "No sample to pick from"
                : "Add a field from the sample…"}
            </option>
            {offered.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </div>
      </Field>
      <Field
        label="Editable fields"
        error={msgs(diags, "editable")}
        hint="Input values the approver can change before approving. Downstream nodes get the edited values."
      >
        <div className="space-y-1.5">
          {editable.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {editable.map((f) => (
                <button
                  key={f}
                  type="button"
                  title="Make this field read-only"
                  className="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-[11px] hover:border-bad"
                  onClick={() =>
                    onChange((n) =>
                      setOpt(n, "editable", toggleField(editable, f)),
                    )
                  }
                >
                  {f} ×
                </button>
              ))}
            </div>
          )}
          <Select
            aria-label="Add an editable field"
            value=""
            onChange={(e) =>
              e.target.value &&
              onChange((n) =>
                setOpt(n, "editable", toggleField(editable, e.target.value)),
              )
            }
          >
            <option value="">
              {paths.length === 0
                ? "No sample to pick from"
                : "Make a field editable…"}
            </option>
            {paths
              .filter((p) => !editable.includes(p))
              .map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
          </Select>
        </div>
      </Field>
      <Field label="Priority" error={msgs(diags, "priority")}>
        <Select
          value={node.priority ?? "normal"}
          onChange={(e) =>
            onChange((n) => ({
              ...n,
              priority: e.target.value as ApprovalNode["priority"],
            }))
          }
        >
          {PRIORITIES.map((p) => (
            <option key={p}>{p}</option>
          ))}
        </Select>
      </Field>
      <Field
        label="Timeout"
        error={msgs(diags, "timeout")}
        hint="Empty waits until someone decides, e.g. 24h."
      >
        <TextInput
          value={node.timeout ?? ""}
          placeholder="none"
          onChange={(e) =>
            onChange((n) => setOpt(n, "timeout", e.target.value))
          }
        />
      </Field>
      <Field
        label="When it times out"
        error={msgs(diags, "on_timeout")}
        hint="denied and approved route to that port; error fails the node."
      >
        <Select
          value={node.on_timeout ?? "denied"}
          disabled={!node.timeout}
          onChange={(e) =>
            onChange((n) =>
              setOpt(
                n,
                "on_timeout",
                e.target.value === "denied" ? "" : e.target.value,
              ),
            )
          }
        >
          {ON_TIMEOUTS.map((o) => (
            <option key={o}>{o}</option>
          ))}
        </Select>
      </Field>
      <div className="space-y-1.5" data-section="approval-preview">
        <h3 className="m-0 text-xs font-semibold">Preview</h3>
        {preview === undefined ? (
          <p className="m-0 text-[11px] text-muted">
            No sample input yet: run the flow once to see the approval.
          </p>
        ) : (
          <div className="space-y-2 rounded-md border border-line p-2">
            <div className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate text-xs font-medium">
                {preview.title || "(empty title)"}
              </span>
              <PriorityBadge priority={preview.priority} />
            </div>
            <RequestBody request={{ ...preview, dry_run: false }} />
            {preview.editable.length > 0 && (
              <EditFieldsPreview
                key={JSON.stringify(preview.editable)}
                editable={preview.editable}
              />
            )}
            <div className="text-[11px] text-muted">
              {preview.timeout
                ? `Expires after ${preview.timeout}, then continues on ${preview.on_timeout === "error" ? "the error port" : `port ${preview.on_timeout}`}.`
                : "Waits until someone decides."}
            </div>
          </div>
        )}
      </div>
    </>
  );
}

export function Inspector(props: InspectorProps) {
  const { node, diagnostics } = props;
  const [draftId, setDraftId] = useState(node.id);
  const idError =
    draftId !== node.id &&
    (draftId.trim() === "" || props.idTaken(draftId.trim()));
  const nodeLevel = diagnostics.filter((d) => {
    const field = diagnosticField(d);
    // Approval nodes have no section for this field, so show it here.
    return (
      field === undefined ||
      field === "edges" ||
      (node.type === "approval" && field === "requires_approval")
    );
  });

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
          flow={props.flow}
          onRenamePort={props.onRenamePort}
          onRemovePort={props.onRemovePort}
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
      {node.type === "approval" && (
        <ApprovalForm
          node={node}
          flowId={props.flowId}
          diags={diagnostics}
          onChange={(p) =>
            props.onChange((n) => (n.type === "approval" ? p(n) : n))
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
      {node.type !== "approval" && (
        <RequiresApprovalSection
          node={node}
          diags={diagnostics}
          onChange={props.onChange}
        />
      )}
    </div>
  );
}
