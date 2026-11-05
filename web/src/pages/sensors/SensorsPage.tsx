import { useCallback, useState, type MouseEvent } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { useSensors } from "../../api/sensors";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
} from "../../components/ui";
import { NewSensorForm } from "./NewSensorForm";
import { SensorEditorPane } from "./SensorEditor";
import { SensorSidebar, SensorTable } from "./SensorList";

const DISCARD = "Discard unsaved changes to this sensor?";

export function SensorsPage() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const creating = params.get("new") === "1";
  const navigate = useNavigate();
  const sensors = useSensors();
  const [dirty, setDirty] = useState(false);
  const onDirtyChange = useCallback((d: boolean) => setDirty(d), []);

  /** Ask before a link drops unsaved edits. */
  const guard = (e: MouseEvent) => {
    if (dirty && !window.confirm(DISCARD)) e.preventDefault();
  };
  const startCreate = () => {
    if (dirty && !window.confirm(DISCARD)) return;
    void navigate("/sensors?new=1");
  };

  const items = sensors.data?.items ?? [];
  const failing = items.filter((s) => s.status.state === "failing").length;
  const invalid = items.filter((s) => s.status.state === "invalid").length;
  const subtitle = sensors.data
    ? [
        `${items.length} sensor${items.length === 1 ? "" : "s"}`,
        failing > 0 && `${failing} failing`,
        invalid > 0 && `${invalid} invalid`,
      ]
        .filter(Boolean)
        .join(" · ")
    : undefined;

  let body;
  if (sensors.isPending) {
    body = <LoadingState label="Loading sensors" />;
  } else if (sensors.isError) {
    body = (
      <ErrorState
        error={sensors.error}
        onRetry={() => void sensors.refetch()}
      />
    );
  } else if (creating) {
    body = (
      <NewSensorForm
        existingIds={items.map((s) => s.id)}
        onCreated={(created) => void navigate(`/sensors/${created}`)}
        onCancel={() => void navigate(id ? `/sensors/${id}` : "/sensors")}
      />
    );
  } else if (id) {
    body = (
      <div className="flex items-start">
        <aside
          aria-label="Sensors"
          className="w-64 shrink-0 self-stretch border-r border-line"
        >
          <SensorSidebar sensors={items} selected={id} guard={guard} />
        </aside>
        <section className="min-w-0 flex-1">
          <SensorEditorPane
            id={id}
            onDirtyChange={onDirtyChange}
            onDeleted={() => void navigate("/sensors")}
          />
        </section>
      </div>
    );
  } else if (items.length === 0) {
    body = (
      <EmptyState
        title="No sensors"
        hint="A sensor polls a source on a schedule and turns new items into events. Sensor files live under sensors/."
        action={
          <Button variant="primary" icon="plus" onClick={startCreate}>
            New sensor
          </Button>
        }
      />
    );
  } else {
    body = <SensorTable sensors={items} guard={guard} />;
  }

  return (
    <>
      <PageHeader
        title="Sensors"
        subtitle={subtitle}
        actions={
          <Button icon="plus" onClick={startCreate} disabled={creating}>
            New sensor
          </Button>
        }
      />
      {body}
    </>
  );
}
