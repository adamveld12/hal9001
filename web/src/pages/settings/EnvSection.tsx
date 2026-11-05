import {
  useDeleteEnvKey,
  useEnvKeys,
  useSetEnvValue,
} from "../../api/settings";
import { EnvEditor } from "./EnvEditor";

/** Keys in the `env` file. Secret values are write-only; plain values are shown. */
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
        data: keys.data,
        refetch: keys.refetch,
      }}
      set={set}
      remove={remove}
      listTitle="Environment keys"
      description="Injected into every node process and redacted from stored output unless marked not secret. Secret values (the default) are write-only: never shown, even here. Plain values are shown and not redacted."
      emptyHint="Add a key below, for example a token a command node needs."
    />
  );
}
