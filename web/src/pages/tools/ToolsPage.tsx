import { useCallback, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
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
import { usePageContext } from "../../layout/pageContext";
import { useAssist } from "../assist/context";
import { sameTarget } from "../assist/drawerState";
import { toolsListContext } from "../assist/pageContexts";
import { ToolDraftPane } from "./ToolDraftPane";
import { ToolEditorPane } from "./ToolEditor";

const DISCARD = "Discard unsaved changes to this tool?";
const NEW_TOOL = { kind: "tool", id: null } as const;

export function ToolsPage() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const creating = params.get("new") === "1";
  const assist = useAssist();
  // The list page holds the editor of a new tool while the drawer is on one.
  const draftingWithAi =
    id === undefined && assist.open && sameTarget(assist.target, NEW_TOOL);
  const navigate = useNavigate();
  const tools = useTools();
  const [dirty, setDirty] = useState(false);
  const onDirtyChange = useCallback((d: boolean) => setDirty(d), []);

  const confirmLeave = () => !dirty || window.confirm(DISCARD);
  const items = tools.data?.items ?? [];
  // The editors publish their own; the list does while it is what is shown.
  usePageContext(
    tools.data && id === undefined && !creating && !draftingWithAi
      ? toolsListContext(items)
      : null,
  );
  const invalid = items.filter((t) =>
    t.diagnostics.some((d) => d.severity === "error"),
  ).length;

  let body;
  if (tools.isPending) body = <LoadingState label="Loading tools" />;
  else if (tools.isError)
    body = (
      <ErrorState error={tools.error} onRetry={() => void tools.refetch()} />
    );
  else if (creating || draftingWithAi) {
    body = (
      <ToolDraftPane
        onDirtyChange={onDirtyChange}
        onCreated={(created) => {
          // The chat stays attached: the drawer's target moves to the saved tool.
          if (sameTarget(assist.target, NEW_TOOL)) {
            assist.retarget({ kind: "tool", id: created });
          }
          void navigate(`/tools/${created}`);
        }}
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
    <div className="h-full min-w-0 overflow-auto">
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
              onClick={() => confirmLeave() && void navigate("/tools?new=1")}
            >
              New tool
            </Button>
            <Button
              icon="agent"
              disabled={creating || draftingWithAi}
              onClick={() => {
                if (!confirmLeave()) return;
                if (id !== undefined) void navigate("/tools");
                assist.openTarget(NEW_TOOL);
              }}
            >
              Create with AI
            </Button>
          </>
        }
      />
      {body}
    </div>
  );
}
