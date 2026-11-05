import {
  useDeleteSensorEnv,
  useSensorEnv,
  useSetSensorEnv,
} from "../../api/sensors";
import { EnvEditor } from "../settings/EnvEditor";

/**
 * The sensor's own environment (`env.d/sensors/<id>.env`, not in the sensor
 * YAML). Values are write-only: nothing here can read one back.
 */
export function SensorEnvSection({ sensorId }: { sensorId: string }) {
  const keys = useSensorEnv(sensorId);
  const set = useSetSensorEnv(sensorId);
  const remove = useDeleteSensorEnv(sensorId);
  return (
    <EnvEditor
      keys={{
        isPending: keys.isPending,
        isError: keys.isError,
        isSuccess: keys.isSuccess,
        error: keys.error,
        data: keys.data?.map((k) => ({
          key: k.key,
          overridesGlobal: k.overrides_global,
        })),
        refetch: keys.refetch,
      }}
      set={set}
      remove={remove}
      listTitle="Environment"
      description="Set for this sensor's polls only, on top of the global env file (a key set here wins). Stored outside the sensor YAML, redacted from stored output, applied on the next poll. Values are write-only: they are never shown, even here."
      emptyHint="Add a key below, for example LINEAR_API_KEY for the Linear sensor."
      namePlaceholder="LINEAR_API_KEY"
    />
  );
}
