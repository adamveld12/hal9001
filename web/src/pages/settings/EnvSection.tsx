import {
  useDeleteEnvKey,
  useEnvKeys,
  useSetEnvValue,
} from "../../api/settings";
import { EnvEditor } from "./EnvEditor";

/** Names in the `env` file. Values are write-only: nothing here can read one back. */
export function EnvSection() {
  const keys = useEnvKeys();
  const set = useSetEnvValue();
  const remove = useDeleteEnvKey();
  return (
    <EnvEditor
      keys={{
        isPending: keys.isPending,
        isError: keys.isError,
        isSuccess: keys.isSuccess,
        error: keys.error,
        data: keys.data?.map((key) => ({ key })),
        refetch: keys.refetch,
      }}
      set={set}
      remove={remove}
      listTitle="Environment keys"
      description="Injected into every node process and redacted from stored output. Values are write-only: they are never shown, even here."
      emptyHint="Add a key below, for example a token a command node needs."
    />
  );
}
