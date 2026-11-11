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
import { usePageContext } from "../../layout/pageContext";
import { useAssist } from "../assist/context";
import { sensorsListContext } from "../assist/pageContexts";
import { NewSensorForm } from "./NewSensorForm";
import { SensorDraftPane } from "./SensorDraftPane";
import { SensorEditorPane } from "./SensorEditor";
import { SensorSidebar, SensorTable } from "./SensorList";

const DISCARD = "Discard unsaved changes to this sensor?";

export function SensorsPage() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const creating = params.get("new") === "1";
  const assist = useAssist();
  // The list page holds the editor of a new sensor while the drawer is on one.
  const drafting =
    id === undefined &&
    !creating &&
    assist.open &&
    assist.target.kind === "sensor" &&
    assist.target.id === null;
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
  const startCreateWithAi = () => {
    if (dirty && !window.confirm(DISCARD)) return;
    if (id !== undefined) void navigate("/sensors");
    assist.openTarget({ kind: "sensor", id: null });
  };

  const items = sensors.data?.items ?? [];
  // The editors publish their own; the list does while it is what is shown.
  usePageContext(
    sensors.data && id === undefined && !creating && !drafting
      ? sensorsListContext(items)
      : null,
  );
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
            otherIds={items.map((s) => s.id).filter((x) => x !== id)}
            onRenamed={(newId) =>
              void navigate(
                {
                  pathname: `/sensors/${newId}`,
                  search: window.location.search,
                },
                { replace: true },
              )
            }
          />
        </section>
      </div>
    );
  } else if (drafting) {
    body = (
      <SensorDraftPane
        onDirtyChange={onDirtyChange}
        onCreated={(created) => {
          // The chat stays attached: the drawer's target moves to the saved sensor.
          assist.retarget({ kind: "sensor", id: created });
          void navigate(`/sensors/${created}`);
        }}
      />
    );
  } else if (items.length === 0) {
    body = (
      <EmptyState
        title="No sensors"
        hint="A sensor polls a source on a schedule and turns new items into events. Sensor files live under sensors/."
        action={
          <div className="flex gap-2">
            <Button variant="primary" icon="plus" onClick={startCreate}>
              New sensor
            </Button>
            <Button icon="agent" onClick={startCreateWithAi}>
              Create with AI
            </Button>
          </div>
        }
      />
    );
  } else {
    body = <SensorTable sensors={items} guard={guard} />;
  }

  return (
    <div className="h-full min-w-0 overflow-auto">
      <PageHeader
        title="Sensors"
        subtitle={subtitle}
        actions={
          <>
            <Button icon="plus" onClick={startCreate} disabled={creating}>
              New sensor
            </Button>
            <Button
              icon="agent"
              onClick={startCreateWithAi}
              disabled={creating || drafting}
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
