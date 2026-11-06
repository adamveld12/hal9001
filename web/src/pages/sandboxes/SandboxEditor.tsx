import { useState, type ReactNode } from "react";
import { errorMessage } from "../../api/client";
import {
  useCheckSandbox,
  useDeleteSandboxEnv,
  useSandbox,
  useSandboxEnv,
  useSetSandboxEnv,
  useUpdateSandbox,
  type SandboxBackend,
  type SandboxConfig,
  type SandboxDetail,
} from "../../api/sandboxes";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  Select,
  TextArea,
  TextInput,
  Toggle,
} from "../../components/ui";
import { diagnosticMessages, isConflict } from "../settings/problem";
import { EnvEditor } from "../settings/EnvEditor";
import { RulesEditor } from "./RulesEditor";
import {
  configFromForm,
  formFromConfig,
  type SandboxForm,
} from "./sandboxForm";

const CREDENTIALS: Record<SandboxBackend, string> = {
  local: "Commands run on this Mac with the global env plus the keys below.",
  docker: "Optional DOCKER_HOST selects the Engine socket.",
  depot:
    "Needs DEPOT_TOKEN (secret) and, for app or multi-organization tokens, DEPOT_ORG_ID. They are never passed to commands.",
  kubernetes: "Commands in the pods see the global env plus the keys below.",
  ssh: "Needs SSH_PASSWORD, or SSH_PRIVATE_KEY (and SSH_KEY_PASSPHRASE), unless a key file is set. They are never passed to commands.",
};

const ENV_PLACEHOLDER: Record<SandboxBackend, string> = {
  local: "GITHUB_TOKEN",
  docker: "DOCKER_HOST",
  depot: "DEPOT_TOKEN",
  kubernetes: "KUBECONFIG_TOKEN",
  ssh: "SSH_PASSWORD",
};

export function SandboxEditor({ backend }: { backend: SandboxBackend }) {
  const detail = useSandbox(backend);
  if (detail.isPending) return <LoadingState />;
  if (detail.isError)
    return (
      <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
    );
  return (
    <ConfigForm
      key={backend}
      backend={backend}
      detail={detail.data}
      onReload={() => void detail.refetch()}
    />
  );
}

interface Draft {
  form: SandboxForm;
  baseHash: string;
}

function ConfigForm({
  backend,
  detail,
  onReload,
}: {
  backend: SandboxBackend;
  detail: SandboxDetail;
  onReload: () => void;
}) {
  const update = useUpdateSandbox(backend);
  const check = useCheckSandbox(backend);
  const [draft, setDraft] = useState<Draft | null>(null);

  const form = draft?.form ?? formFromConfig(detail.config);
  const parsed = configFromForm(form);
  const errors = "errors" in parsed ? parsed.errors : {};
  const stale =
    (draft !== null && draft.baseHash !== detail.hash) ||
    isConflict(update.error);
  const dirty = draft !== null;

  const editForm = (patch: Partial<SandboxForm>) => {
    setDraft({
      form: { ...form, ...patch },
      baseHash: draft?.baseHash ?? detail.hash,
    });
    check.reset();
  };
  const edit = (patch: Partial<SandboxConfig>) =>
    editForm({ config: { ...form.config, ...patch } });
  const c = form.config;

  const reload = () => {
    setDraft(null);
    update.reset();
    check.reset();
    onReload();
  };
  const save = () => {
    if (!("config" in parsed) || stale) return;
    update.mutate(
      { expected_hash: detail.hash, config: parsed.config },
      { onSuccess: () => setDraft(null) },
    );
  };
  const runCheck = () => {
    if ("config" in parsed) check.mutate(parsed.config);
  };
  const diagnostics = diagnosticMessages(update.error);
  const fileProblems = detail.diagnostics.filter((d) => d.severity === "error");

  return (
    <div className="space-y-4" data-backend={backend}>
      {stale && (
        <div
          role="alert"
          className="flex items-center gap-3 rounded-md border border-warn/40 bg-warn/5 p-3 text-xs"
        >
          <span>
            sandboxes/{backend}.yaml changed after you started editing. Reload
            to see the current values; your edits are discarded.
          </span>
          <Button className="ml-auto" icon="refresh" onClick={reload}>
            Reload
          </Button>
        </div>
      )}
      {fileProblems.length > 0 && (
        <div
          role="alert"
          className="rounded-md border border-bad/40 bg-bad/5 p-3 text-xs text-bad"
        >
          The file on disk has errors, so the defaults are shown:
          <ul className="m-0 mt-1 list-none space-y-0.5 p-0">
            {fileProblems.map((d) => (
              <li key={`${d.path}${d.message}`}>
                {d.path ? `${d.path}: ` : ""}
                {d.message}
              </li>
            ))}
          </ul>
        </div>
      )}

      <Panel
        title="Sandbox"
        actions={
          <Badge tone={detail.configured ? "ok" : "muted"}>
            {detail.configured ? "configured" : "not configured"}
          </Badge>
        }
      >
        <div className="space-y-3">
          <Toggle
            label="Enabled"
            checked={c.enabled}
            onChange={(v) => edit({ enabled: v })}
          />
          <div className="grid grid-cols-2 gap-3">
            <Field label="Lifecycle" hint={lifecycleHint(backend, c.lifecycle)}>
              <Select
                value={c.lifecycle}
                onChange={(e) =>
                  edit({
                    lifecycle: e.target.value as SandboxConfig["lifecycle"],
                  })
                }
              >
                <option value="per_run">per run</option>
                <option value="persistent">persistent</option>
              </Select>
            </Field>
            <Field
              label="Command timeout"
              hint="Default for one command, for example 90s or 10m."
            >
              <TextInput
                value={c.timeout}
                onChange={(e) => edit({ timeout: e.target.value })}
              />
            </Field>
          </div>
        </div>
      </Panel>

      <BackendFields
        backend={backend}
        form={form}
        errors={errors}
        edit={edit}
        editForm={editForm}
      />

      <Panel title="Approval">
        <div className="space-y-3">
          <Field
            label="Policy"
            hint="Applies to the shell and file-write tools of this sandbox. Reading, listing and searching never need approval."
          >
            <Select
              value={c.approval}
              onChange={(e) =>
                edit({ approval: e.target.value as SandboxConfig["approval"] })
              }
            >
              <option value="auto">auto: run without asking</option>
              <option value="always">always: ask in the Inbox</option>
              <option value="rules">rules: decide by command and path</option>
            </Select>
          </Field>
          {c.approval === "rules" && (
            <RulesEditor
              rules={c.approval_rules}
              onChange={(approval_rules) => edit({ approval_rules })}
            />
          )}
        </div>
      </Panel>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="primary"
          icon="save"
          disabled={!dirty || "errors" in parsed || stale || update.isPending}
          onClick={save}
        >
          {update.isPending ? "Saving…" : "Save"}
        </Button>
        <Button
          disabled={!dirty || update.isPending}
          onClick={() => {
            setDraft(null);
            update.reset();
            check.reset();
          }}
        >
          Discard
        </Button>
        <Button
          icon="play"
          disabled={"errors" in parsed || check.isPending}
          onClick={runCheck}
        >
          {check.isPending ? "Checking…" : "Check connection"}
        </Button>
        {update.isError && !isConflict(update.error) && (
          <span role="alert" className="text-xs text-bad">
            {errorMessage(update.error)}
          </span>
        )}
        {update.isSuccess && !dirty && (
          <span className="text-xs text-ok">Saved.</span>
        )}
      </div>
      {diagnostics.length > 0 && (
        <ul className="m-0 list-none space-y-1 p-0 text-xs text-bad">
          {diagnostics.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      )}
      <CheckResultView
        backend={backend}
        check={check}
        pinned={c.host_key}
        onPin={(fp) => edit({ host_key: fp })}
      />

      <EnvSection backend={backend} />
    </div>
  );
}

function lifecycleHint(backend: SandboxBackend, lifecycle: string): string {
  const per: Record<SandboxBackend, string> = {
    local:
      "A directory per run under the root, removed after a successful run.",
    docker: "A container per run, removed after a successful run.",
    depot: "A Depot sandbox per run, stopped at the end.",
    kubernetes: "A pod per run, deleted after a successful run.",
    ssh: "A directory per run under the root, removed after a successful run.",
  };
  const persistent: Record<SandboxBackend, string> = {
    local: "All runs share the root directory.",
    docker: "One long-lived container per image; a workdir per run.",
    depot: "One sandbox kept alive; a workdir per run.",
    kubernetes: "One pod per image kept running; a workdir per run.",
    ssh: "All runs share the root directory.",
  };
  return lifecycle === "persistent" ? persistent[backend] : per[backend];
}

type EditFn = (patch: Partial<SandboxConfig>) => void;

function BackendFields({
  backend,
  form,
  errors,
  edit,
  editForm,
}: {
  backend: SandboxBackend;
  form: SandboxForm;
  errors: Partial<Record<keyof SandboxForm, string>>;
  edit: EditFn;
  editForm: (p: Partial<SandboxForm>) => void;
}) {
  const c = form.config;
  const text = (
    key: keyof SandboxConfig,
    label: string,
    hint?: ReactNode,
    placeholder?: string,
  ) => (
    <Field label={label} hint={hint}>
      <TextInput
        value={String(c[key] ?? "")}
        placeholder={placeholder}
        onChange={(e) => edit({ [key]: e.target.value })}
      />
    </Field>
  );
  const images = (
    <Field
      label="Images"
      hint="One per line. The agent node picks one; the first is the default."
      error={errors.imagesText ? [errors.imagesText] : undefined}
    >
      <TextArea
        rows={3}
        value={form.imagesText}
        onChange={(e) => editForm({ imagesText: e.target.value })}
      />
    </Field>
  );
  const resources = (
    <div className="grid grid-cols-3 gap-3">
      <Field label="CPUs" hint="Empty: default." error={opt(errors.cpus)}>
        <TextInput
          inputMode="decimal"
          value={form.cpus}
          onChange={(e) => editForm({ cpus: e.target.value })}
        />
      </Field>
      <Field label="Memory (MB)" error={opt(errors.memory_mb)}>
        <TextInput
          inputMode="numeric"
          value={form.memory_mb}
          onChange={(e) => editForm({ memory_mb: e.target.value })}
        />
      </Field>
      {backend === "depot" && (
        <Field label="Disk (GB)" error={opt(errors.disk_gb)}>
          <TextInput
            inputMode="numeric"
            value={form.disk_gb}
            onChange={(e) => editForm({ disk_gb: e.target.value })}
          />
        </Field>
      )}
    </div>
  );

  switch (backend) {
    case "local":
      return (
        <Panel title="Local">
          {text(
            "root",
            "Root directory",
            "Absolute path. Empty uses data/sandboxes/local in the hal9001 home.",
            "/Users/you/hal-sandbox",
          )}
        </Panel>
      );
    case "docker":
      return (
        <Panel title="Docker">
          <div className="space-y-3">
            {text(
              "socket",
              "Socket",
              "Engine socket, unix:///path or tcp://host:port. Empty uses DOCKER_HOST, then the usual socket locations.",
              "/var/run/docker.sock",
            )}
            {images}
            <Toggle
              label="Network access"
              checked={c.network}
              onChange={(v) => edit({ network: v })}
            />
            {resources}
          </div>
        </Panel>
      );
    case "depot":
      return (
        <Panel title="Depot">
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              {text(
                "org_id",
                "Organization ID",
                "Needed for app or multi-organization tokens. Empty uses DEPOT_ORG_ID.",
              )}
              <Field label="Architecture">
                <Select
                  value={c.architecture}
                  onChange={(e) =>
                    edit({
                      architecture: e.target
                        .value as SandboxConfig["architecture"],
                    })
                  }
                >
                  <option value="">default (amd64)</option>
                  <option value="amd64">amd64</option>
                  <option value="arm64">arm64</option>
                </Select>
              </Field>
            </div>
            {text(
              "image_ref",
              "Image",
              "A Depot snapshot image. Empty boots Depot's default image.",
              "registry.depot.dev/…",
            )}
            <div className="grid grid-cols-3 gap-3">
              <Field
                label="Lifetime (minutes)"
                hint="Empty: Depot default (120)."
                error={opt(errors.timeout_minutes)}
              >
                <TextInput
                  inputMode="numeric"
                  value={form.timeout_minutes}
                  onChange={(e) =>
                    editForm({ timeout_minutes: e.target.value })
                  }
                />
              </Field>
              <Field label="vCPUs" error={opt(errors.cpus)}>
                <TextInput
                  inputMode="numeric"
                  value={form.cpus}
                  onChange={(e) => editForm({ cpus: e.target.value })}
                />
              </Field>
              <Field label="Memory (MB)" error={opt(errors.memory_mb)}>
                <TextInput
                  inputMode="numeric"
                  value={form.memory_mb}
                  onChange={(e) => editForm({ memory_mb: e.target.value })}
                />
              </Field>
            </div>
            <Field label="Disk (GB)" error={opt(errors.disk_gb)}>
              <TextInput
                inputMode="numeric"
                value={form.disk_gb}
                onChange={(e) => editForm({ disk_gb: e.target.value })}
              />
            </Field>
            <div className="flex gap-4">
              <Toggle
                label="Disable tailnet"
                checked={c.disable_tailnet}
                onChange={(v) => edit({ disable_tailnet: v })}
              />
              <Toggle
                label="Disable Depot cache"
                checked={c.disable_depot_cache}
                onChange={(v) => edit({ disable_depot_cache: v })}
              />
            </div>
            <Field
              label="Depot CI secrets"
              hint="One ENV_NAME=secret-name per line: injects that Depot CI secret as the variable."
              error={opt(errors.secretsText)}
            >
              <TextArea
                rows={3}
                value={form.secretsText}
                invalid={!!errors.secretsText}
                onChange={(e) => editForm({ secretsText: e.target.value })}
              />
            </Field>
            {text(
              "endpoint",
              "API endpoint",
              undefined,
              "https://api.depot.dev",
            )}
          </div>
        </Panel>
      );
    case "kubernetes":
      return (
        <Panel title="Kubernetes">
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              {text(
                "kubeconfig",
                "Kubeconfig",
                "Path. Empty uses kubectl's default.",
                "~/.kube/config",
              )}
              {text("context", "Context", "Empty uses the current context.")}
              {text("namespace", "Namespace", undefined, "default")}
              {text("service_account", "Service account")}
            </div>
            {images}
            {resources}
          </div>
        </Panel>
      );
    case "ssh":
      return (
        <Panel title="SSH">
          <div className="space-y-3">
            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2">{text("host", "Host")}</div>
              <Field label="Port" error={opt(errors.port)}>
                <TextInput
                  inputMode="numeric"
                  value={form.port}
                  placeholder="22"
                  onChange={(e) => editForm({ port: e.target.value })}
                />
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              {text("user", "User")}
              {text(
                "key_file",
                "Key file",
                "Optional. Otherwise SSH_PRIVATE_KEY or SSH_PASSWORD from the environment below.",
                "~/.ssh/id_ed25519",
              )}
            </div>
            {text(
              "root",
              "Root directory",
              "On the remote host; relative paths start in its home directory.",
            )}
            <Field
              label="Host key fingerprint"
              hint="Pinned. Use Check connection to see the server's fingerprint and pin it after you have compared it."
            >
              <TextInput
                className="font-mono"
                value={c.host_key}
                placeholder="SHA256:…"
                onChange={(e) => edit({ host_key: e.target.value.trim() })}
              />
            </Field>
          </div>
        </Panel>
      );
  }
}

const opt = (m: string | undefined) => (m ? [m] : undefined);

function CheckResultView({
  backend,
  check,
  pinned,
  onPin,
}: {
  backend: SandboxBackend;
  check: ReturnType<typeof useCheckSandbox>;
  pinned: string;
  onPin: (fingerprint: string) => void;
}) {
  if (check.isError)
    return (
      <p role="alert" className="text-xs text-bad">
        {errorMessage(check.error)}
      </p>
    );
  if (!check.isSuccess) return null;
  const r = check.data;
  const needsPin =
    backend === "ssh" && r.fingerprint && r.fingerprint !== pinned;
  return (
    <div
      role="status"
      data-check={r.ok ? "ok" : "failed"}
      className="space-y-2 rounded-md border border-line bg-panel p-3 text-xs"
    >
      <div className="flex items-center gap-2">
        <Badge tone={r.ok ? "ok" : "bad"}>{r.ok ? "OK" : "Failed"}</Badge>
        <span>{r.message}</span>
      </div>
      {needsPin && r.fingerprint && (
        <div className="flex flex-wrap items-center gap-2">
          <code className="font-mono">{r.fingerprint}</code>
          <Button icon="lock" onClick={() => onPin(r.fingerprint ?? "")}>
            Pin this host key
          </Button>
          <span className="text-muted">
            Then Save and check again. Only pin it if it matches the server's
            key (<code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code>).
          </span>
        </div>
      )}
    </div>
  );
}

function EnvSection({ backend }: { backend: SandboxBackend }) {
  const keys = useSandboxEnv(backend);
  const set = useSetSandboxEnv(backend);
  const remove = useDeleteSandboxEnv(backend);
  return (
    <EnvEditor
      keys={{
        isPending: keys.isPending,
        isError: keys.isError,
        isSuccess: keys.isSuccess,
        error: keys.error,
        data: keys.data?.map((k) => ({
          key: k.key,
          secret: k.secret,
          value: k.value,
          overridesGlobal: k.overrides_global,
        })),
        refetch: keys.refetch,
      }}
      set={set}
      remove={remove}
      listTitle="Environment"
      description={`Added on top of the global env file for this sandbox (a key set here wins). ${CREDENTIALS[backend]} Secret values (the default) are redacted from everything the agent sees and are write-only: never shown, even here.`}
      emptyHint="Add a key below."
      namePlaceholder={ENV_PLACEHOLDER[backend]}
    />
  );
}
