import { useState } from "react";
import { Link } from "react-router";
import { errorMessage } from "../../api/client";
import { useInjectEvent, useSensorOptions } from "../../api/events";
import { Button, Field, Select, TextArea } from "../../components/ui";
import { Modal } from "./Modal";
import { parseInjectBody } from "./inject";

export function InjectDialog({
  defaultSensor,
  onClose,
}: {
  defaultSensor: string;
  onClose: () => void;
}) {
  const sensors = useSensorOptions();
  const inject = useInjectEvent();
  const [chosen, setChosen] = useState(defaultSensor);
  const [text, setText] = useState("{\n  \n}");
  const [parseError, setParseError] = useState<string>();

  const options = sensors.data?.filter((s) => s.valid) ?? [];
  const sensorId = chosen || options[0]?.id || "";

  const submit = () => {
    const parsed = parseInjectBody(text);
    if (!parsed.ok) {
      setParseError(parsed.error);
      return;
    }
    setParseError(undefined);
    inject.mutate({ sensorId, item: parsed.value });
  };

  const result = inject.data;

  return (
    <Modal
      title="Inject event"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{result ? "Close" : "Cancel"}</Button>
          <Button
            variant="primary"
            icon="play"
            disabled={!sensorId || inject.isPending}
            onClick={submit}
          >
            {inject.isPending ? "Injecting…" : "Inject"}
          </Button>
        </>
      }
    >
      <Field
        label="Sensor"
        hint="The item goes through the sensor's key template, dedup, and flow fan-out like a polled item."
      >
        {sensors.isError ? (
          <span className="text-xs text-bad">
            {errorMessage(sensors.error)}
          </span>
        ) : (
          <Select
            value={sensorId}
            disabled={sensors.isPending}
            onChange={(e) => setChosen(e.target.value)}
          >
            {options.length === 0 && (
              <option value="">
                {sensors.isPending ? "Loading…" : "No valid sensors"}
              </option>
            )}
            {options.map((s) => (
              <option key={s.id} value={s.id}>
                {s.id}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <Field label="Item (JSON)" error={parseError ? [parseError] : undefined}>
        <TextArea
          rows={10}
          spellCheck={false}
          invalid={parseError !== undefined}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      </Field>
      {inject.isError && (
        <div role="alert" className="text-xs text-bad">
          {errorMessage(inject.error)}
        </div>
      )}
      {result && (
        <div className="rounded-md border border-line bg-raised p-2 text-xs">
          {result.deduplicated ? (
            <>
              Already seen (dedup key <code>{result.dedup_key}</code>); nothing
              created.{" "}
            </>
          ) : (
            <>Event created with {result.flow_runs.length} flow run(s). </>
          )}
          <Link
            className="text-accent hover:underline"
            to={`/events/${result.event.id}`}
            onClick={onClose}
          >
            Open event
          </Link>
        </div>
      )}
    </Modal>
  );
}
