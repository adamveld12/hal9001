import { useMemo } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { ApiError } from "../../api/client";
import { useFlowVersion, useFlowVersions } from "../../api/runs";
import { CodeEditor } from "../../components/CodeEditor";
import { CopyButton } from "../../components/CopyButton";
import { Tabs } from "../../components/Tabs";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
} from "../../components/ui";
import { FlowCanvas } from "../../flow/FlowCanvas";
import { flowFromApi } from "../../flow/apiAdapter";
import { flowToGraph } from "../../flow/graph";
import { cx } from "../../lib/cx";
import { shortId } from "../../lib/format";
import { Mono, TextLink } from "./Meta";

type TabKey = "graph" | "yaml";

const TABS = [
  { key: "graph", label: "Graph" },
  { key: "yaml", label: "YAML" },
] as const;

function VersionList({ flowId, hash }: { flowId: string; hash: string }) {
  const q = useFlowVersions(flowId);
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <aside
      aria-label="Versions"
      className="w-72 shrink-0 overflow-y-auto border-l border-line bg-panel"
    >
      <div className="border-b border-line px-3 py-2 text-xs font-semibold">
        Versions
      </div>
      {q.isPending && <LoadingState label="Loading versions" />}
      {q.isError && (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      )}
      {q.isSuccess && items.length === 0 && (
        <p className="m-0 p-3 text-xs text-muted">No versions recorded.</p>
      )}
      <ul className="m-0 list-none divide-y divide-line p-0 text-xs">
        {items.map((v) => (
          <li key={v.hash}>
            <Link
              to={`/flows/${flowId}/versions/${v.hash}`}
              aria-current={v.hash === hash ? "page" : undefined}
              className={cx(
                "flex items-center gap-2 px-3 py-1.5 hover:bg-raised",
                v.hash === hash && "bg-raised",
              )}
            >
              <Mono>{shortId(v.hash)}</Mono>
              <Time ts={v.created_at} />
              <span className="ml-auto text-muted">
                {v.run_count} {v.run_count === 1 ? "run" : "runs"}
              </span>
            </Link>
          </li>
        ))}
      </ul>
      {q.hasNextPage && (
        <div className="p-3">
          <Button
            disabled={q.isFetchingNextPage}
            onClick={() => void q.fetchNextPage()}
          >
            {q.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </aside>
  );
}

export function FlowVersionPage() {
  const { id = "", hash = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const tab: TabKey = params.get("tab") === "yaml" ? "yaml" : "graph";

  const q = useFlowVersion(id, hash);
  const graph = useMemo(
    () => (q.data?.flow ? flowToGraph(flowFromApi(q.data.flow)) : null),
    [q.data],
  );

  const header = (
    <PageHeader
      title={
        <span className="flex items-center gap-2">
          {id}
          <Badge>
            version <Mono>{shortId(hash)}</Mono>
          </Badge>
        </span>
      }
      subtitle={
        q.data && (
          <>
            saved <Time ts={q.data.created_at} /> · {q.data.run_count}{" "}
            {q.data.run_count === 1 ? "run" : "runs"}
          </>
        )
      }
      actions={<TextLink to={`/flows/${id}`}>Open current in editor</TextLink>}
    />
  );

  if (q.isError) {
    return (
      <div>
        {header}
        {q.error instanceof ApiError && q.error.status === 404 ? (
          <EmptyState
            title="Version not found"
            hint="It may have been pruned by retention."
            action={<TextLink to={`/flows/${id}`}>Open the flow</TextLink>}
          />
        ) : (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        )}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {header}
      <div className="flex min-h-0 flex-1">
        <div className="flex min-w-0 flex-1 flex-col">
          <Tabs
            tabs={TABS}
            active={tab}
            onChange={(key) =>
              setParams(
                (prev) => {
                  const next = new URLSearchParams(prev);
                  next.set("tab", key);
                  return next;
                },
                { replace: true },
              )
            }
          />
          <div className="relative min-h-0 flex-1 overflow-auto">
            {q.isPending && <LoadingState label="Loading version" />}
            {q.isSuccess && tab === "graph" && graph && (
              <FlowCanvas nodes={graph.nodes} edges={graph.edges} readOnly />
            )}
            {q.isSuccess && tab === "graph" && !graph && (
              <EmptyState
                title="This version does not parse"
                hint="The graph cannot be drawn; see the YAML tab."
              />
            )}
            {q.isSuccess && tab === "yaml" && (
              <div className="space-y-2 p-3">
                <div className="flex justify-end">
                  <CopyButton text={q.data.yaml} label="Copy YAML" />
                </div>
                <CodeEditor
                  readOnly
                  lang="yaml"
                  value={q.data.yaml}
                  minHeight="12rem"
                  maxHeight="75vh"
                  label="Flow YAML"
                />
              </div>
            )}
          </div>
        </div>
        <VersionList flowId={id} hash={hash} />
      </div>
    </div>
  );
}
