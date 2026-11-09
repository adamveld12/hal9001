import { useCallback, useState, type MouseEvent } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { usePrompts } from "../../api/prompts";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
} from "../../components/ui";
import { NewPromptForm } from "./NewPromptForm";
import { PromptEditorPane } from "./PromptEditor";
import { PromptSidebar, PromptTable } from "./PromptList";

const DISCARD = "Discard unsaved changes to this prompt?";

export function PromptsPage() {
  const { name } = useParams();
  const [params] = useSearchParams();
  const creating = params.get("new") === "1";
  const navigate = useNavigate();
  const prompts = usePrompts();
  const [dirty, setDirty] = useState(false);
  const onDirtyChange = useCallback((d: boolean) => setDirty(d), []);

  /** Ask before a link drops unsaved edits. */
  const guard = (e: MouseEvent) => {
    if (dirty && !window.confirm(DISCARD)) e.preventDefault();
  };
  const startCreate = () => {
    if (dirty && !window.confirm(DISCARD)) return;
    void navigate("/prompts?new=1");
  };

  const items = prompts.data?.items ?? [];

  let body;
  if (prompts.isPending) {
    body = <LoadingState label="Loading prompts" />;
  } else if (prompts.isError) {
    body = (
      <ErrorState
        error={prompts.error}
        onRetry={() => void prompts.refetch()}
      />
    );
  } else if (creating) {
    body = (
      <NewPromptForm
        existingNames={items.map((p) => p.name)}
        onCreated={(created) => void navigate(`/prompts/${created}`)}
        onCancel={() => void navigate(name ? `/prompts/${name}` : "/prompts")}
      />
    );
  } else if (name) {
    body = (
      <div className="flex items-start">
        <aside
          aria-label="Prompts"
          className="w-64 shrink-0 self-stretch border-r border-line"
        >
          <PromptSidebar prompts={items} selected={name} guard={guard} />
        </aside>
        <section className="min-w-0 flex-1">
          <PromptEditorPane
            name={name}
            onDirtyChange={onDirtyChange}
            onDeleted={() => void navigate("/prompts")}
            onRenamed={(newName) =>
              void navigate(
                {
                  pathname: `/prompts/${newName}`,
                  search: window.location.search,
                },
                { replace: true },
              )
            }
          />
        </section>
      </div>
    );
  } else if (items.length === 0) {
    body = (
      <EmptyState
        title="No prompts"
        hint="Agent nodes select a prompt from this library. Prompt files live under prompts/."
        action={
          <Button variant="primary" icon="plus" onClick={startCreate}>
            New prompt
          </Button>
        }
      />
    );
  } else {
    body = <PromptTable prompts={items} guard={guard} />;
  }

  return (
    <>
      <PageHeader
        title="Prompts"
        subtitle={
          prompts.data
            ? `${items.length} prompt${items.length === 1 ? "" : "s"}`
            : undefined
        }
        actions={
          <Button icon="plus" onClick={startCreate} disabled={creating}>
            New prompt
          </Button>
        }
      />
      {body}
    </>
  );
}
