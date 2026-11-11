import { useState } from "react";
import {
  usePreviewAuthStacks,
  useAuthStacks,
  useSetAuthStacks,
  type AuthStacks,
  type AuthStacksPreview,
} from "../../api/auth";
import { errorMessage } from "../../api/client";
import { Button, ErrorState, LoadingState, Panel } from "../../components/ui";
import {
  isDirty,
  toDraft,
  toRequest,
  draftIssues,
  type Draft,
} from "./authStacksDraft";
import { ChangeStatus } from "./ChangeStatus";
import { ConfirmAuthDialog } from "./ConfirmAuthDialog";
import { StacksEditor } from "./StacksEditor";
import { StacksPreviewView } from "./StacksPreviewView";
import { useConfirmedChange } from "./useConfirmedChange";

/** Auth stacks and services as the config file has them: collapsed until asked for. */
export function AuthAdvancedCard() {
  const [open, setOpen] = useState(false);
  return (
    <Panel
      title="Advanced"
      actions={
        <Button
          aria-expanded={open}
          aria-controls="auth-advanced-body"
          icon={open ? "up" : "down"}
          data-testid="auth-advanced-toggle"
          onClick={() => setOpen(!open)}
        >
          {open ? "Hide" : "Show"}
        </Button>
      }
    >
      <p className="m-0 text-xs text-muted">
        Stacks decide which modules admit a request to each service. The
        switches above edit them for you; change them here only for a setup they
        cannot express. A wrong setup can lock remote devices out.
      </p>
      <div id="auth-advanced-body" hidden={!open} className="mt-3">
        {open && <StacksSection />}
      </div>
    </Panel>
  );
}

/** The user's unsaved edit, with the file state it started from. */
interface Edit {
  draft: Draft;
  base: Draft;
  baseHash: string;
}

interface Reviewed {
  /** The request the preview was made for; a different draft makes the preview stale. */
  key: string;
  preview: AuthStacksPreview;
}

function StacksSection() {
  const stacks = useAuthStacks();
  if (stacks.isPending) return <LoadingState />;
  if (stacks.isError) {
    return (
      <ErrorState error={stacks.error} onRetry={() => void stacks.refetch()} />
    );
  }
  return <StacksForm server={stacks.data} />;
}

function StacksForm({ server }: { server: AuthStacks }) {
  const previewCall = usePreviewAuthStacks();
  const setStacks = useSetAuthStacks();
  const change = useConfirmedChange(setStacks.mutateAsync);
  // null: show what the server has; otherwise the edit in progress.
  const [edit, setEdit] = useState<Edit | null>(null);
  const [reviewed, setReviewed] = useState<Reviewed | null>(null);
  const [reviewError, setReviewError] = useState<string | null>(null);

  const serverDraft = toDraft(server);
  const draft = edit?.draft ?? serverDraft;
  const baseHash = edit?.baseHash ?? server.hash;
  const dirty = edit !== null && isDirty(edit.draft, edit.base);
  const stale = edit !== null && dirty && edit.baseHash !== server.hash;
  const issues = draftIssues(draft);
  const requestKey = JSON.stringify(toRequest(draft, baseHash));
  const current = reviewed?.key === requestKey ? reviewed.preview : null;

  const onChange = (fn: (d: Draft) => Draft) => {
    setReviewError(null);
    change.dismiss();
    setEdit((prev) => {
      const from = prev ?? {
        draft: serverDraft,
        base: serverDraft,
        baseHash: server.hash,
      };
      return { ...from, draft: fn(from.draft) };
    });
  };

  const discard = () => {
    setEdit(null);
    setReviewed(null);
    setReviewError(null);
    change.dismiss();
  };

  const review = async () => {
    setReviewError(null);
    const body = toRequest(draft, baseHash);
    try {
      const preview = await previewCall.mutateAsync(body);
      setReviewed({ key: JSON.stringify(body), preview });
    } catch (e) {
      setReviewed(null);
      setReviewError(errorMessage(e));
    }
  };

  const save = async () => {
    const outcome = await change.run(toRequest(draft, baseHash));
    if (outcome === "done") {
      setEdit(null);
      setReviewed(null);
    }
  };

  const busy = previewCall.isPending || change.busy;
  const canSave = dirty && current !== null && current.valid && current.changed;

  return (
    <div className="space-y-3">
      <StacksEditor draft={draft} disabled={busy} onChange={onChange} />

      {stale && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn/5 p-2 text-xs"
        >
          <span>
            config.yaml changed since you started editing. Saving will be
            refused until you start again from the current file.
          </span>
          <Button onClick={discard}>Discard my changes and reload</Button>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button
          icon="search"
          data-testid="stacks-review"
          disabled={!dirty || busy || issues.length > 0}
          onClick={() => void review()}
        >
          {previewCall.isPending ? "Checking…" : "Review changes"}
        </Button>
        <Button
          variant="primary"
          icon="save"
          data-testid="stacks-save"
          disabled={!canSave || busy}
          title={
            canSave
              ? undefined
              : "Review the changes first; errors block saving"
          }
          onClick={() => void save()}
        >
          Save
        </Button>
        {dirty && (
          <Button disabled={busy} onClick={discard}>
            Discard changes
          </Button>
        )}
      </div>

      {reviewError && (
        <p role="alert" className="m-0 text-xs text-bad">
          Could not review: {reviewError}
        </p>
      )}
      {dirty && current === null && !reviewError && (
        <p className="m-0 text-xs text-muted">
          Review the changes to see what will be written.
        </p>
      )}
      {dirty && current && <StacksPreviewView preview={current} />}
      <ChangeStatus
        result={change.result}
        error={change.error}
        done="Stacks saved."
      />

      <ConfirmAuthDialog
        confirmation={change.confirmation}
        busy={change.busy}
        onCancel={change.cancel}
        onConfirm={() =>
          void change.confirm().then((outcome) => {
            if (outcome === "done") {
              setEdit(null);
              setReviewed(null);
            }
          })
        }
      />
    </div>
  );
}
