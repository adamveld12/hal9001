import { useNavigate } from "react-router";
import { errorMessage } from "../../api/client";
import { Time } from "../../components/Time";
import { Button, StatusBadge } from "../../components/ui";
import { shortId } from "../../lib/format";
import { Mono, SectionTitle, TextLink } from "./Meta";
import { CONTINUE_DISABLED_REASON, canContinue, chatPath } from "./continue";
import { useForkConversation } from "../../api/conversations";
import { useContinuationsOf, useForksOf } from "./forks";
import type { Schemas } from "../../api/client";

/** Forks the agent run into a chat and opens it. Disabled with the reason while the run is active. */
export function ContinueInChatButton({
  run,
}: {
  run: Pick<Schemas["AgentRun"], "id" | "status">;
}) {
  const navigate = useNavigate();
  const fork = useForkConversation();
  const allowed = canContinue(run);
  return (
    <span className="inline-flex flex-col items-start gap-1">
      <Button
        variant="primary"
        disabled={!allowed || fork.isPending}
        title={allowed ? undefined : CONTINUE_DISABLED_REASON}
        onClick={() =>
          fork.mutate(run.id, {
            onSuccess: (c) => void navigate(chatPath(c.id)),
          })
        }
      >
        {fork.isPending ? "Continuing…" : "Continue in chat"}
      </Button>
      {!allowed && (
        <span className="text-[11px] text-muted">
          {CONTINUE_DISABLED_REASON}
        </span>
      )}
      {fork.isError && (
        <span role="alert" className="max-w-72 text-[11px] text-bad">
          {errorMessage(fork.error)}
        </span>
      )}
    </span>
  );
}

/** Chats forked from the run; renders nothing when there are none. */
export function ForksSection({ agentRunId }: { agentRunId: string }) {
  const q = useForksOf(agentRunId);
  const items = q.data ?? [];
  if (items.length === 0) return null;
  return (
    <section>
      <SectionTitle>Continued in chat</SectionTitle>
      <ul className="m-0 list-none space-y-1 p-0 text-xs">
        {items.map((c) => (
          <li key={c.id} className="flex flex-wrap items-center gap-2">
            <TextLink to={chatPath(c.id)}>{c.title}</TextLink>
            <StatusBadge status={c.status} />
            <span className="ml-auto text-muted">
              <Time ts={c.updated_at} />
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Continuation runs of an original flow run; renders nothing when there are none. */
export function ContinuedBySection({ flowRunId }: { flowRunId: string }) {
  const q = useContinuationsOf(flowRunId);
  const items = q.data ?? [];
  if (items.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line px-4 py-1.5 text-xs">
      <span className="font-semibold">Continued by</span>
      {items.map((r) => (
        <span key={r.id} className="inline-flex items-center gap-1.5">
          <TextLink to={`/runs/${r.id}`}>
            <Mono>{shortId(r.id)}</Mono>
          </TextLink>
          <StatusBadge status={r.status} />
          {r.continuation && (
            <span className="text-muted">
              {r.continuation.node_id} → {r.continuation.port}
            </span>
          )}
        </span>
      ))}
    </div>
  );
}
