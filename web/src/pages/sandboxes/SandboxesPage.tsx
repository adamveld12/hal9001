import { Navigate, useNavigate, useParams } from "react-router";
import {
  SANDBOX_BACKENDS,
  useSandboxes,
  type SandboxBackend,
} from "../../api/sandboxes";
import { Badge, PageHeader } from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList } from "../../lib/contextSummary";
import { cx } from "../../lib/cx";
import { SandboxEditor } from "./SandboxEditor";

function isBackend(v: string | undefined): v is SandboxBackend {
  return SANDBOX_BACKENDS.some((b) => b.key === v);
}

/** Configure → Sandboxes: one tab per backend. */
export function SandboxesPage() {
  const { backend } = useParams<{ backend: string }>();
  const navigate = useNavigate();
  const list = useSandboxes();
  usePageContext(
    list.data
      ? {
          title: "Sandboxes",
          facts: factList([["selected backend", backend]]),
          entities: list.data.map((s) => ({
            kind: "sandbox_backend",
            id: s.backend,
            detail: `${s.enabled ? "enabled" : "disabled"}, ${s.configured ? "configured" : "not configured"}, ${s.valid ? "valid" : "invalid"}, lifecycle ${s.lifecycle}, approval ${s.approval}`,
          })),
        }
      : null,
  );
  if (backend === undefined) return <Navigate to="/sandboxes/local" replace />;
  if (!isBackend(backend)) return <Navigate to="/sandboxes/local" replace />;
  const summary = (key: string) => list.data?.find((s) => s.backend === key);

  return (
    <>
      <PageHeader
        title="Sandboxes"
        subtitle="Run logs appear on the agent run and flow run pages under “Sandbox”."
      />
      <div className="mx-auto max-w-4xl space-y-4 p-4">
        <div
          role="tablist"
          className="flex gap-1 border-b border-line"
          aria-label="Sandbox backends"
        >
          {SANDBOX_BACKENDS.map((b) => {
            const s = summary(b.key);
            return (
              <button
                key={b.key}
                type="button"
                role="tab"
                aria-selected={b.key === backend}
                onClick={() => void navigate(`/sandboxes/${b.key}`)}
                className={cx(
                  "-mb-px flex items-center gap-1.5 border-b-2 px-2.5 py-2 text-xs font-medium",
                  b.key === backend
                    ? "border-accent text-fg"
                    : "border-transparent text-muted hover:text-fg",
                )}
              >
                {b.label}
                {s?.enabled && <Badge tone="ok">on</Badge>}
              </button>
            );
          })}
        </div>
        <SandboxEditor backend={backend} />
      </div>
    </>
  );
}
