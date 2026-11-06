import { useState } from "react";
import { Link } from "react-router";
import { errorMessage } from "../../api/client";
import { useFlowOptions } from "../../api/events";
import { useClearQueue, useQueue, useRemoveFromQueue } from "../../api/queue";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Time } from "../../components/Time";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Select,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { shortId } from "../../lib/format";
import {
  clearConfirmText,
  pruneSelection,
  removeNote,
  toggleAll,
  toggleSelected,
  waitingText,
} from "./queueView";

/**
 * Runs waiting for a slot, in the order the engine claims them. Removing a run
 * cancels it (it stays in history). `flow` is the optional flow filter; its
 * storage belongs to the caller.
 */
export function FlowQueue({
  flow,
  onFlowChange,
}: {
  flow: string;
  onFlowChange: (flow: string) => void;
}) {
  const queue = useQueue(flow);
  const flows = useFlowOptions();
  const remove = useRemoveFromQueue();
  const clear = useClearQueue();
  const [picked, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [confirming, setConfirming] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const items = queue.data?.items;
  // A run that started or was removed drops out of the selection.
  const selected = pruneSelection(picked, items ?? []);

  const flowIds = flows.data?.map((f) => f.id) ?? [];
  if (flow && !flowIds.includes(flow)) flowIds.unshift(flow);

  const removeRuns = (ids: readonly string[]) =>
    remove.mutate(ids, {
      onSuccess: (r) => {
        setNote(removeNote(r));
        setSelected(
          (s) => new Set([...s].filter((id) => !r.removed.includes(id))),
        );
      },
      onError: (e) => setNote(errorMessage(e)),
    });

  const total = queue.data?.total ?? 0;
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2">
        <Select
          className="w-40"
          aria-label="Flow"
          value={flow}
          onChange={(e) => onFlowChange(e.target.value)}
        >
          <option value="">All flows</option>
          {flowIds.map((id) => (
            <option key={id} value={id}>
              {id}
            </option>
          ))}
        </Select>
        <Button
          icon="trash"
          disabled={selected.size === 0 || remove.isPending}
          onClick={() => removeRuns([...selected])}
        >
          Remove selected{selected.size > 0 ? ` (${selected.size})` : ""}
        </Button>
        <Button
          variant="danger"
          disabled={total === 0 || clear.isPending}
          onClick={() => setConfirming(true)}
        >
          Clear queue
        </Button>
        {note && (
          <span role="status" className="text-xs text-muted">
            {note}
          </span>
        )}
      </div>
      {queue.isPending ? (
        <LoadingState label="Loading queue" />
      ) : queue.isError && !queue.data ? (
        <ErrorState error={queue.error} onRetry={() => void queue.refetch()} />
      ) : items && items.length === 0 ? (
        <EmptyState
          title="The queue is empty"
          hint="Runs wait here when a flow, agent or command limit is reached, or while the kill switch is engaged."
        />
      ) : (
        queue.data && (
          <Table>
            <thead>
              <tr>
                <Th>
                  <input
                    type="checkbox"
                    aria-label="Select all"
                    checked={
                      queue.data.items.length > 0 &&
                      selected.size === queue.data.items.length
                    }
                    onChange={() =>
                      setSelected((s) => toggleAll(s, queue.data.items))
                    }
                  />
                </Th>
                <Th>#</Th>
                <Th>Flow</Th>
                <Th>Run</Th>
                <Th>Trigger event</Th>
                <Th>Queued for</Th>
                <Th>Waiting for</Th>
                <Th />
              </tr>
            </thead>
            <tbody>
              {queue.data.items.map((it) => {
                const id = it.flow_run.id;
                return (
                  <tr key={id} className="hover:bg-raised/60">
                    <Td>
                      <input
                        type="checkbox"
                        aria-label={`Select run ${shortId(id)}`}
                        checked={selected.has(id)}
                        onChange={() =>
                          setSelected((s) => toggleSelected(s, id))
                        }
                      />
                    </Td>
                    <Td className="tabular-nums">{it.position}</Td>
                    <Td>
                      <Link
                        to={`/flows/${it.flow_run.flow_id}`}
                        className="font-medium text-accent"
                      >
                        {it.flow_run.flow_id}
                      </Link>
                    </Td>
                    <Td>
                      <Link
                        to={`/runs/${id}`}
                        className="font-mono text-accent"
                      >
                        {shortId(id)}
                      </Link>
                    </Td>
                    <Td>
                      {it.flow_run.event_id ? (
                        <Link
                          to={`/events/${it.flow_run.event_id}`}
                          className="font-mono text-accent"
                        >
                          {shortId(it.flow_run.event_id)}
                        </Link>
                      ) : (
                        <span className="text-muted">–</span>
                      )}
                    </Td>
                    <Td>
                      <Time ts={it.queued_at} />
                    </Td>
                    <Td>{waitingText(it, queue.data.limits)}</Td>
                    <Td>
                      <Button
                        variant="ghost"
                        disabled={remove.isPending}
                        onClick={() => removeRuns([id])}
                      >
                        Remove
                      </Button>
                    </Td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        )
      )}
      <ConfirmDialog
        open={confirming}
        title="Clear the queue"
        confirmLabel="Clear queue"
        busy={clear.isPending}
        onCancel={() => setConfirming(false)}
        onConfirm={() =>
          clear.mutate(flow, {
            onSuccess: (r) => {
              setConfirming(false);
              setSelected(new Set());
              setNote(
                `Cancelled ${r.cancelled} ${r.cancelled === 1 ? "run" : "runs"}.`,
              );
            },
            onError: (e) => {
              setConfirming(false);
              setNote(errorMessage(e));
            },
          })
        }
      >
        {clearConfirmText(total, flow)}
      </ConfirmDialog>
    </div>
  );
}
