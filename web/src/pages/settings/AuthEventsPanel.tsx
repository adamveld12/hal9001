import { useAuthEvents, type AuthEvent } from "../../api/auth";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Panel,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { statusTone } from "../../lib/status";
import { actionLabel } from "./authView";

export function AuthEventsPanel() {
  const events = useAuthEvents(100);
  const list = events.data?.events ?? [];
  return (
    <Panel
      title="Recent auth events"
      actions={
        <Button
          icon="refresh"
          disabled={events.isFetching}
          onClick={() => void events.refetch()}
        >
          Refresh
        </Button>
      }
    >
      {events.isPending && <LoadingState />}
      {events.isError && (
        <ErrorState
          error={events.error}
          onRetry={() => void events.refetch()}
        />
      )}
      {events.isSuccess && list.length === 0 && (
        <EmptyState title="No auth events yet" />
      )}
      {events.isSuccess && list.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>Time</Th>
              <Th>Action</Th>
              <Th>Service</Th>
              <Th>Endpoint</Th>
              <Th>Outcome</Th>
              <Th>Modules</Th>
              <Th>Principal</Th>
              <Th>From</Th>
            </tr>
          </thead>
          <tbody>
            {list.map((e) => (
              <EventRow key={e.id} event={e} />
            ))}
          </tbody>
        </Table>
      )}
    </Panel>
  );
}

function EventRow({ event: e }: { event: AuthEvent }) {
  return (
    <tr>
      <Td>
        <Time ts={e.ts} />
      </Td>
      <Td className="whitespace-nowrap">{actionLabel(e.action)}</Td>
      <Td>{e.service ?? "–"}</Td>
      <Td className="max-w-56 truncate font-mono">
        <span title={e.endpoint}>{e.endpoint}</span>
      </Td>
      <Td>
        <StatusBadge status={e.outcome} />
        {e.reason && (
          <div className="mt-0.5 max-w-56 break-words text-[11px] text-muted">
            {e.reason}
          </div>
        )}
      </Td>
      <Td>
        <div className="flex flex-wrap gap-1">
          {(e.modules ?? []).map((m, i) => (
            <Badge
              key={`${i}-${m.module}`}
              tone={statusTone(m.result)}
              className="whitespace-nowrap"
            >
              <span title={m.reason ? `${m.control}: ${m.reason}` : m.control}>
                {m.module}: {m.result}
              </span>
            </Badge>
          ))}
        </div>
      </Td>
      <Td>{e.principal || "–"}</Td>
      <Td className="text-muted">
        {[e.host, e.remote_addr].filter(Boolean).join(" · ") || "–"}
      </Td>
    </tr>
  );
}
