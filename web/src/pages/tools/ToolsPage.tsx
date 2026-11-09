import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useAssistSession } from "../../api/assist";
import { useTools } from "../../api/tools";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { AssistDrawer } from "../assist/AssistDrawer";
import {
  AssistPageContext,
  type AssistPage,
  type EditorBridge,
} from "../assist/context";
import { targetConfig } from "../assist/targets";
import { ToolDraftPane } from "./ToolDraftPane";
import { ToolEditorPane } from "./ToolEditor";

const DISCARD = "Discard unsaved changes to this tool?";
const NEW = "new";

export function ToolsPage() {
  const { id } = useParams();
  const [params, setParams] = useSearchParams();
  const creating = params.get("new") === "1";
  const assistParam = creating ? null : params.get("assist");
  const sessionId =
    assistParam !== null && assistParam !== NEW ? assistParam : null;
  const navigate = useNavigate();
  const tools = useTools();
  const [dirty, setDirty] = useState(false);
  const onDirtyChange = useCallback((d: boolean) => setDirty(d), []);
  const editorRef = useRef<EditorBridge | null>(null);

  const setAssist = useCallback(
    (value: string | null, replace: boolean) =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (value === null) next.delete("assist");
          else next.set("assist", value);
          return next;
        },
        { replace },
      ),
    [setParams],
  );
  const assist = useMemo<AssistPage>(
    () => ({
      kind: "tool",
      param: assistParam,
      sessionId,
      targetId: id ?? null,
      open: () => setAssist(NEW, false),
      close: () => setAssist(null, false),
      bind: (sid) => setAssist(sid, true),
      registerEditor: (e) => {
        editorRef.current = e;
      },
      editor: () => editorRef.current,
    }),
    [assistParam, sessionId, id, setAssist],
  );

  // A link to a session names no tool: go to the one it edits.
  const linked = useAssistSession(id === undefined ? sessionId : null);
  const linkedKind = linked.data?.target.kind;
  const linkedTarget = linked.data?.target.id ?? null;
  useEffect(() => {
    if (sessionId === null || linkedKind === undefined) return;
    if (linkedKind !== "tool") {
      const cfg = targetConfig(linkedKind);
      void navigate(
        linkedTarget === null
          ? cfg.draftPath(sessionId)
          : cfg.editorPath(linkedTarget, sessionId),
        { replace: true },
      );
    } else if (id === undefined && linkedTarget !== null) {
      void navigate(`/tools/${linkedTarget}?assist=${sessionId}`, {
        replace: true,
      });
    }
  }, [id, sessionId, linkedKind, linkedTarget, navigate]);

  const confirmLeave = () => !dirty || window.confirm(DISCARD);
  const items = tools.data?.items ?? [];
  const invalid = items.filter((t) =>
    t.diagnostics.some((d) => d.severity === "error"),
  ).length;

  let body;
  if (tools.isPending) body = <LoadingState label="Loading tools" />;
  else if (tools.isError)
    body = (
      <ErrorState error={tools.error} onRetry={() => void tools.refetch()} />
    );
  else if (creating || (id === undefined && assistParam !== null)) {
    body = (
      <ToolDraftPane
        onDirtyChange={onDirtyChange}
        onCreated={(created, sid) =>
          void navigate(
            sid === null
              ? `/tools/${created}`
              : `/tools/${created}?assist=${sid}`,
          )
        }
      />
    );
  } else if (id) {
    body = (
      <div className="flex items-start">
        <aside
          aria-label="Tools"
          className="w-56 shrink-0 self-stretch border-r border-line"
        >
          <ul className="m-0 list-none p-1">
            {items.map((t) => (
              <li key={t.id}>
                <Link
                  to={`/tools/${t.id}`}
                  aria-current={t.id === id ? "page" : undefined}
                  onClick={(e) => !confirmLeave() && e.preventDefault()}
                  className={`block truncate rounded-md px-2 py-1 font-mono text-xs hover:bg-raised ${t.id === id ? "bg-raised font-semibold" : ""}`}
                >
                  {t.id}
                </Link>
              </li>
            ))}
          </ul>
        </aside>
        <section className="min-w-0 flex-1">
          <ToolEditorPane
            id={id}
            onDirtyChange={onDirtyChange}
            onDeleted={() => void navigate("/tools")}
            onRenamed={(newId) =>
              void navigate(
                { pathname: `/tools/${newId}`, search: window.location.search },
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
        title="No tools"
        hint="A tool is a script an agent can call. Tool files live under tools/."
        action={
          <Button
            variant="primary"
            icon="plus"
            onClick={() => void navigate("/tools?new=1")}
          >
            New tool
          </Button>
        }
      />
    );
  } else {
    body = (
      <Table>
        <thead>
          <tr>
            <Th>Tool</Th>
            <Th>Description</Th>
            <Th>Language</Th>
            <Th>Approval</Th>
            <Th>Problems</Th>
          </tr>
        </thead>
        <tbody>
          {items.map((t) => (
            <tr key={t.id} className="hover:bg-raised">
              <Td>
                <Link
                  className="font-mono text-accent hover:underline"
                  to={`/tools/${t.id}`}
                >
                  {t.id}
                </Link>
              </Td>
              <Td className="max-w-md truncate">{t.tool?.description ?? ""}</Td>
              <Td>{t.tool?.lang ?? "–"}</Td>
              <Td>
                {t.tool
                  ? t.tool.approve === "never"
                    ? "never"
                    : typeof t.tool.approve === "object"
                      ? "rules"
                      : "always"
                  : "–"}
              </Td>
              <Td>
                {t.diagnostics.some((d) => d.severity === "error") ? (
                  <Badge tone="bad">invalid</Badge>
                ) : (
                  <span className="text-muted">–</span>
                )}
              </Td>
            </tr>
          ))}
        </tbody>
      </Table>
    );
  }

  return (
    <AssistPageContext.Provider value={assist}>
      <div className="flex h-full">
        <div className="min-w-0 flex-1 overflow-auto">
          <PageHeader
            title="Tools"
            subtitle={
              tools.data
                ? `${items.length} tool${items.length === 1 ? "" : "s"}${invalid > 0 ? ` · ${invalid} invalid` : ""}`
                : undefined
            }
            actions={
              <>
                <Button
                  icon="plus"
                  disabled={creating}
                  onClick={() =>
                    confirmLeave() && void navigate("/tools?new=1")
                  }
                >
                  New tool
                </Button>
                <Button
                  icon="agent"
                  disabled={
                    creating || (id === undefined && assistParam !== null)
                  }
                  onClick={() =>
                    confirmLeave() && void navigate(`/tools?assist=${NEW}`)
                  }
                >
                  Create with AI
                </Button>
              </>
            }
          />
          {body}
        </div>
        {assistParam !== null && <AssistDrawer />}
      </div>
    </AssistPageContext.Provider>
  );
}
