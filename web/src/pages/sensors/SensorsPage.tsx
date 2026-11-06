import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
} from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { useAssistSession } from "../../api/assist";
import { useSensors } from "../../api/sensors";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
} from "../../components/ui";
import { AssistDrawer } from "../assist/AssistDrawer";
import {
  AssistPageContext,
  type AssistPage,
  type EditorBridge,
} from "../assist/context";
import { targetConfig } from "../assist/targets";
import { NewSensorForm } from "./NewSensorForm";
import { SensorDraftPane } from "./SensorDraftPane";
import { SensorEditorPane } from "./SensorEditor";
import { SensorSidebar, SensorTable } from "./SensorList";

const DISCARD = "Discard unsaved changes to this sensor?";
const DISCARD_NEW =
  "Close the assistant and leave this new sensor? Text not saved as a sensor is dropped here; the session keeps its draft.";
const NEW = "new";

export function SensorsPage() {
  const { id } = useParams();
  const [params, setParams] = useSearchParams();
  const creating = params.get("new") === "1";
  const assistParam = creating ? null : params.get("assist");
  const sessionId =
    assistParam !== null && assistParam !== NEW ? assistParam : null;
  const navigate = useNavigate();
  const sensors = useSensors();
  const [dirty, setDirty] = useState(false);
  const onDirtyChange = useCallback((d: boolean) => setDirty(d), []);

  const editorRef = useRef<EditorBridge | null>(null);
  const setAssist = useCallback(
    (value: string | null, replace: boolean) =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (value === null) next.delete("assist");
          else next.set("assist", value);
          return next;
        },
        { replace },
      ),
    [setParams],
  );
  const assist = useMemo<AssistPage>(
    () => ({
      kind: "sensor",
      param: assistParam,
      sessionId,
      targetId: id ?? null,
      open: () => setAssist(NEW, false),
      close: () => {
        // Closing the new-sensor page would drop its unsaved text; the session itself keeps the draft.
        if (id === undefined && dirty && !window.confirm(DISCARD_NEW)) return;
        setAssist(null, false);
      },
      bind: (sid) => setAssist(sid, true),
      registerEditor: (e) => {
        editorRef.current = e;
      },
      editor: () => editorRef.current,
    }),
    [assistParam, sessionId, id, dirty, setAssist],
  );

  // A link to a session (an agent run, an approval) names no sensor: go to the one it edits.
  const linked = useAssistSession(id === undefined ? sessionId : null);
  const linkedKind = linked.data?.target.kind;
  const linkedTarget = linked.data?.target.id ?? null;
  useEffect(() => {
    if (sessionId === null || linkedKind === undefined) return;
    if (linkedKind === "flow") {
      void navigate(
        linkedTarget === null
          ? targetConfig("flow").draftPath(sessionId)
          : targetConfig("flow").editorPath(linkedTarget, sessionId),
        { replace: true },
      );
    } else if (id === undefined && linkedTarget !== null) {
      void navigate(`/sensors/${linkedTarget}?assist=${sessionId}`, {
        replace: true,
      });
    }
  }, [id, sessionId, linkedKind, linkedTarget, navigate]);

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
    void navigate(`/sensors?assist=${NEW}`);
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
  } else if (assistParam !== null) {
    body = (
      <SensorDraftPane
        onDirtyChange={onDirtyChange}
        onCreated={(created, sid) =>
          void navigate(
            sid === null
              ? `/sensors/${created}`
              : `/sensors/${created}?assist=${sid}`,
          )
        }
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
    <AssistPageContext.Provider value={assist}>
      <div className="flex h-full">
        <div className="min-w-0 flex-1 overflow-auto">
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
                  disabled={
                    creating || (id === undefined && assistParam !== null)
                  }
                >
                  Create with AI
                </Button>
              </>
            }
          />
          {body}
        </div>
        {assistParam !== null && <AssistDrawer />}
      </div>
    </AssistPageContext.Provider>
  );
}
