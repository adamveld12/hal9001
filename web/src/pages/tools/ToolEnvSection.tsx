import { useDeleteToolEnv, useSetToolEnv, useToolEnv } from "../../api/tools";
import { EnvEditor } from "../settings/EnvEditor";

/** The tool's own environment (`env.d/tools/<id>.env`, not in the tool YAML). */
export function ToolEnvSection({ toolId }: { toolId: string }) {
  const keys = useToolEnv(toolId);
  const set = useSetToolEnv(toolId);
  const remove = useDeleteToolEnv(toolId);
  return (
    <EnvEditor
      keys={{
        isPending: keys.isPending,
        isError: keys.isError,
        isSuccess: keys.isSuccess,
        error: keys.error,
        data: keys.data?.map((k) => ({
          key: k.key,
          secret: k.secret,
          value: k.value,
          overridesGlobal: k.overrides_global,
        })),
        refetch: keys.refetch,
      }}
      set={set}
      remove={remove}
      listTitle="Environment"
      description="Set for this tool's calls only, on top of the global env file (a key set here wins). Stored outside the tool YAML, applied on the next call. Secret values (the default) are redacted from output and write-only."
      emptyHint="Add a key below, for example API_TOKEN."
      namePlaceholder="API_TOKEN"
    />
  );
}
