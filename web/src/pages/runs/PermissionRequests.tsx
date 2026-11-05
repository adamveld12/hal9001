import { usePermissionRequests } from "../../api/runs";
import { JsonView } from "../../components/JsonView";
import { Time } from "../../components/Time";
import {
  Badge,
  EmptyState,
  ErrorState,
  LoadingState,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { Mono, TextLink } from "./Meta";

/** Every permission request of one agent run with its outcome. */
export function PermissionRequests({ agentRunId }: { agentRunId: string }) {
  const q = usePermissionRequests(agentRunId);
  if (q.isPending) return <LoadingState label="Loading permission requests" />;
  if (q.isError)
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.length === 0) {
    return (
      <EmptyState
        title="No permission requests"
        hint="The agent did not ask for permission during this run."
      />
    );
  }
  return (
    <div className="overflow-x-auto">
      <Table>
        <thead>
          <tr>
            <Th>Requested</Th>
            <Th>Tool</Th>
            <Th>Decision</Th>
            <Th>Decided by</Th>
            <Th>Matched rule</Th>
            <Th>Reason</Th>
            <Th>Approval</Th>
          </tr>
        </thead>
        <tbody>
          {q.data.map((r) => (
            <tr key={r.id} className="align-top">
              <Td>
                <Time ts={r.requested_at} />
              </Td>
              <Td>
                <div className="flex items-center gap-1.5">
                  <span className="font-medium">{r.title}</span>
                  <Badge>{r.tool_kind}</Badge>
                </div>
                <details className="mt-1">
                  <summary className="cursor-pointer text-muted">input</summary>
                  <JsonView value={r.raw_input} maxHeight="10rem" />
                </details>
              </Td>
              <Td>
                {r.decision ? (
                  <StatusBadge status={r.decision} />
                ) : (
                  <StatusBadge status="pending" />
                )}
                {r.decided_at && (
                  <div>
                    <Time ts={r.decided_at} />
                  </div>
                )}
              </Td>
              <Td>{r.decided_by ?? <span className="text-muted">–</span>}</Td>
              <Td>
                {r.matched_rule ? (
                  <Mono>{r.matched_rule}</Mono>
                ) : (
                  <span className="text-muted">–</span>
                )}
              </Td>
              <Td className="max-w-64 break-words">
                {r.reason ?? <span className="text-muted">–</span>}
              </Td>
              <Td>
                {r.approval_id ? (
                  <TextLink to={`/approvals/${r.approval_id}`}>open</TextLink>
                ) : (
                  <span className="text-muted">–</span>
                )}
              </Td>
            </tr>
          ))}
        </tbody>
      </Table>
    </div>
  );
}
