import { InlineEdit } from "../../components/InlineEdit";
import { useRenamePrompt } from "../../api/rename";
import { usePrompts } from "../../api/prompts";
import { validateName } from "../../lib/inlineEdit";
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { ApiError, errorMessage } from "../../api/client";
import {
  useDeletePrompt,
  usePrompt,
  useUpdatePrompt,
  type PromptDetail,
} from "../../api/prompts";
import { CodeEditor } from "../../components/CodeEditor";
import { Tabs } from "../../components/Tabs";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
} from "../../components/ui";
import { ConfirmButton } from "../files/ConfirmButton";
import { ConflictBanner } from "../files/ConflictBanner";
import {
  conflictHash,
  diagnosticsOf,
  hasErrors,
  isConflict,
  visibleDiagnostics,
  type Checked,
} from "../files/diagnostics";
import { DiagnosticsList } from "../files/DiagnosticsList";
import { useEditState } from "../files/useEditState";
import { PromptPreview } from "./PromptPreview";

const TABS = [
  { key: "edit", label: "Edit" },
  { key: "preview", label: "Preview" },
] as const;

type TabKey = (typeof TABS)[number]["key"];

function Editor({
  detail,
  onDirtyChange,
  onDeleted,
  onRenamed,
}: {
  detail: PromptDetail;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
  onRenamed: (newName: string) => void;
}) {
  const name = detail.name;
  const rename = useRenamePrompt();
  const otherNames = (usePrompts().data?.items ?? [])
    .map((x) => x.name)
    .filter((x) => x !== name);
  const [params, setParams] = useSearchParams();
  const tab: TabKey = params.get("tab") === "preview" ? "preview" : "edit";
  const { state, dispatch, dirty } = useEditState({
    hash: detail.hash,
    text: detail.markdown,
  });
  const update = useUpdatePrompt();
  const del = useDeletePrompt();
  const prompt = usePrompt(name);
  const [rejected, setRejected] = useState<Checked | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const shown = visibleDiagnostics(
    state.draft,
    { text: detail.markdown, diagnostics: detail.diagnostics },
    rejected,
  );
  const busy = update.isPending || del.isPending;

  const save = (expectedHash: string) => {
    const text = state.draft;
    setNotice(null);
    update.mutate(
      { name, expectedHash, markdown: text },
      {
        onSuccess: (file) => {
          dispatch({
            type: "saved",
            submitted: text,
            file: { hash: file.hash, text: file.markdown },
          });
          setRejected(null);
          setNotice("Saved.");
        },
        onError: (e) => {
          if (isConflict(e)) {
            dispatch({ type: "conflict", currentHash: conflictHash(e) });
            return;
          }
          const ds = diagnosticsOf(e);
          if (ds) setRejected({ text, diagnostics: ds });
        },
      },
    );
  };

  /** Re-read the file to learn its current hash, keep the draft, and save it over. */
  const overwrite = async () => {
    const fresh = await prompt.refetch();
    if (!fresh.data) return;
    dispatch({
      type: "rebase",
      file: { hash: fresh.data.hash, text: fresh.data.markdown },
    });
    save(fresh.data.hash);
  };

  const remove = () =>
    del.mutate(
      { name, expectedHash: state.base.hash },
      {
        onSuccess: onDeleted,
        onError: (e) => {
          if (isConflict(e)) {
            dispatch({ type: "conflict", currentHash: conflictHash(e) });
          }
        },
      },
    );

  const saveError =
    update.error && !isConflict(update.error) && !diagnosticsOf(update.error)
      ? errorMessage(update.error)
      : null;
  const deleteError =
    del.error && !isConflict(del.error) ? errorMessage(del.error) : null;
  const parsed = detail.prompt;

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <InlineEdit
          mono
          label="Prompt name"
          value={name}
          validate={(v) => validateName(v, name, otherNames, "prompt")}
          disabledReason={
            dirty ? "Save or discard your edits before renaming" : null
          }
          onSave={(newName) =>
            rename.mutateAsync(
              { id: name, newId: newName, expectedHash: state.base.hash },
              { onSuccess: () => onRenamed(newName) },
            )
          }
        />
        {parsed && (
          <Badge tone={parsed.output === "note" ? "info" : "muted"}>
            output: {parsed.output ?? "none"}
          </Badge>
        )}
        <span className="text-xs text-muted">{detail.path}</span>
        <div className="ml-auto flex items-center gap-2">
          {notice && !dirty && (
            <span className="text-xs text-ok">{notice}</span>
          )}
          {dirty && <Badge tone="warn">unsaved</Badge>}
          <Button
            variant="primary"
            icon="save"
            disabled={!dirty || busy || state.conflict !== null}
            onClick={() => save(state.base.hash)}
          >
            {update.isPending ? "Saving…" : "Save"}
          </Button>
          <ConfirmButton
            label="Delete"
            confirmLabel="Delete prompt"
            disabled={busy}
            onConfirm={remove}
          />
        </div>
      </div>
      {parsed?.description && (
        <p className="m-0 text-xs text-muted">{parsed.description}</p>
      )}

      {state.conflict && (
        <ConflictBanner
          conflict={state.conflict}
          noun="prompt"
          busy={busy || prompt.isFetching}
          onReload={() => {
            void prompt.refetch().then((r) => {
              if (r.data) {
                dispatch({
                  type: "reload",
                  file: { hash: r.data.hash, text: r.data.markdown },
                });
                setRejected(null);
                setNotice(null);
              }
            });
          }}
          onOverwrite={() => void overwrite()}
        />
      )}

      <Panel className="overflow-hidden">
        <div className="-m-3">
          <Tabs
            tabs={TABS}
            active={tab}
            onChange={(k) =>
              setParams(
                (p) => {
                  const next = new URLSearchParams(p);
                  if (k === "preview") next.set("tab", "preview");
                  else next.delete("tab");
                  return next;
                },
                { replace: true },
              )
            }
          />
          <div className="p-3">
            {tab === "edit" ? (
              <div className="space-y-3">
                <CodeEditor
                  lang="markdown"
                  label={`Prompt ${name} markdown`}
                  value={state.draft}
                  onChange={(v) => dispatch({ type: "edit", draft: v })}
                  invalid={!shown.stale && hasErrors(shown.diagnostics)}
                  minHeight="16rem"
                  maxHeight="36rem"
                />
                {saveError && (
                  <div role="alert" className="text-xs text-bad">
                    {saveError}
                  </div>
                )}
                {deleteError && (
                  <div role="alert" className="text-xs text-bad">
                    {deleteError}
                  </div>
                )}
                <DiagnosticsList
                  diagnostics={shown.diagnostics}
                  source={shown.stale ? undefined : state.draft}
                  stale={shown.stale}
                />
              </div>
            ) : (
              <PromptPreview markdown={state.draft} />
            )}
          </div>
        </div>
      </Panel>
    </div>
  );
}

/** Loads one prompt and renders its editor, with loading, not-found and error states. */
export function PromptEditorPane({
  name,
  onDirtyChange,
  onDeleted,
  onRenamed,
}: {
  name: string;
  onDirtyChange: (dirty: boolean) => void;
  onDeleted: () => void;
  onRenamed: (newName: string) => void;
}) {
  const prompt = usePrompt(name);
  if (prompt.isPending) return <LoadingState label="Loading prompt" />;
  if (prompt.isError) {
    if (prompt.error instanceof ApiError && prompt.error.status === 404) {
      return (
        <EmptyState
          title="Prompt not found"
          hint={`There is no prompts/${name}.md.`}
        />
      );
    }
    return (
      <ErrorState error={prompt.error} onRetry={() => void prompt.refetch()} />
    );
  }
  return (
    <Editor
      key={name}
      detail={prompt.data}
      onDirtyChange={onDirtyChange}
      onDeleted={onDeleted}
      onRenamed={onRenamed}
    />
  );
}
