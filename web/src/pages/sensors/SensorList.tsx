import type { MouseEvent } from "react";
import { Link } from "react-router";
import type { SensorSummary } from "../../api/sensors";
import { Time } from "../../components/Time";
import { Badge, Table, Td, Th } from "../../components/ui";
import { cx } from "../../lib/cx";
import {
  UNSCHEDULED_TITLE,
  sensorRow,
  sensorStateTone,
  unscheduledLabel,
} from "./view";

type Guard = (e: MouseEvent) => void;

function Problems({ problems }: { problems: string[] }) {
  if (problems.length === 0) return <span className="text-muted">–</span>;
  return (
    <span className="flex flex-wrap gap-1">
      {problems.map((p) => (
        <Badge key={p} tone="bad">
          {p}
        </Badge>
      ))}
    </span>
  );
}

/** Full-width table, shown when no sensor is selected. */
export function SensorTable({
  sensors,
  guard,
}: {
  sensors: readonly SensorSummary[];
  guard: Guard;
}) {
  return (
    <Table>
      <thead>
        <tr>
          <Th>State</Th>
          <Th>Sensor</Th>
          <Th>Schedule</Th>
          <Th>Last poll</Th>
          <Th>New (24h)</Th>
          <Th>Next poll</Th>
          <Th>Errors</Th>
        </tr>
      </thead>
      <tbody>
        {sensors.map((s) => {
          const r = sensorRow(s);
          return (
            <tr key={r.id} className="hover:bg-raised">
              <Td>
                <Badge tone={sensorStateTone(r.state)}>{r.state}</Badge>
              </Td>
              <Td>
                <Link
                  to={`/sensors/${r.id}`}
                  onClick={guard}
                  className="font-mono text-accent hover:underline"
                >
                  {r.id}
                </Link>
              </Td>
              <Td className="text-muted">{r.schedule}</Td>
              <Td>
                <Time ts={r.lastPollAt} />
              </Td>
              <Td className="tabular-nums">{r.newItems24h}</Td>
              <Td>
                {unscheduledLabel(r.state, r.nextPollAt) ? (
                  <span className="text-muted" title={UNSCHEDULED_TITLE}>
                    not scheduled
                  </span>
                ) : (
                  <Time ts={r.nextPollAt} />
                )}
              </Td>
              <Td>
                <Problems problems={r.problems} />
              </Td>
            </tr>
          );
        })}
      </tbody>
    </Table>
  );
}

/** Narrow list next to the editor. */
export function SensorSidebar({
  sensors,
  selected,
  guard,
}: {
  sensors: readonly SensorSummary[];
  selected: string;
  guard: Guard;
}) {
  return (
    <ul className="divide-y divide-line">
      {sensors.map((s) => {
        const r = sensorRow(s);
        return (
          <li key={r.id}>
            <Link
              to={`/sensors/${r.id}`}
              onClick={guard}
              aria-current={r.id === selected ? "page" : undefined}
              className={cx(
                "block space-y-0.5 px-3 py-2 text-xs hover:bg-raised",
                r.id === selected && "bg-raised",
              )}
            >
              <span className="flex items-center gap-2">
                <span className="min-w-0 truncate font-mono font-medium">
                  {r.id}
                </span>
                <Badge
                  tone={sensorStateTone(r.state)}
                  className="ml-auto shrink-0"
                >
                  {r.state}
                </Badge>
              </span>
              <span className="flex items-center gap-1.5 text-muted">
                <Time ts={r.lastPollAt} />
                <span aria-hidden="true">·</span>
                <span className="tabular-nums">{r.newItems24h} new</span>
              </span>
              {r.problems.length > 0 && (
                <span className="block truncate text-bad">
                  {r.problems.join(", ")}
                </span>
              )}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
