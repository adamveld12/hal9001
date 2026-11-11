import { useState } from "react";
import {
  useCreateAssistSession,
  useEndAssistSession,
  useSendAssistMessage,
} from "../../api/assist";
import { useRuntimes } from "../../api/settings";
import { Composer } from "./Composer";
import { Header, RuntimeControls, StarterPrompts } from "./ChatParts";
import { useAssist } from "./context";
import type { DrawerTarget } from "./drawerState";
import { saveRuntime, storedRuntime } from "./runtimeStorage";
import { startSession } from "./startSession";
import { targetConfig } from "./targets";

/**
 * Before a session exists: pick runtime and model, or send the first message.
 * `onStarted` gets the new session's id once its first message was accepted:
 * the drawer shows it, the Chat page opens it. `onNewChat` is what `/new`
 * in the message box does.
 */
export function ChatStart({
  target,
  onStarted,
  onNewChat,
}: {
  target: DrawerTarget;
  onStarted: (sessionId: string) => void;
  onNewChat: () => void;
}) {
  const assist = useAssist();
  const { kind, id: targetId } = target;
  const cfg = targetConfig(kind);
  const runtimes = useRuntimes();
  const create = useCreateAssistSession();
  const send = useSendAssistMessage();
  const end = useEndAssistSession();
  const [runtimeChoice, setRuntime] = useState<string | null>(null);
  const [model, setModel] = useState("");
  const names = runtimes.data?.map((r) => r.name) ?? [];
  const remembered = storedRuntime();
  const runtime =
    runtimeChoice ??
    (remembered !== null && names.includes(remembered)
      ? remembered
      : (names[0] ?? ""));

  const start = async (text: string) => {
    saveRuntime(runtime);
    const yaml = await assist.editor()?.getText();
    await startSession({
      create: () =>
        create.mutateAsync({
          target: targetId === null ? { kind } : { kind, id: targetId },
          runtime,
          model: model.trim() === "" ? undefined : model.trim(),
          yaml,
        }),
      send: (id) =>
        send.mutateAsync({ id, text, page_context: assist.pageContext() }),
      end: (id) => end.mutateAsync(id),
      bind: onStarted,
    });
  };

  return (
    <>
      <Header
        title={targetId === null ? cfg.newTitle : cfg.existingTitle(targetId)}
      >
        <RuntimeControls
          runtime={runtime}
          model={model}
          locked={false}
          onRuntime={setRuntime}
          onModel={setModel}
        />
      </Header>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto p-3">
        <p className="m-0 text-xs text-muted">{cfg.intro}</p>
        {runtimes.isError && (
          <div role="alert" className="text-xs text-bad">
            Could not load the runtimes: {runtimes.error.message}
          </div>
        )}
        {runtimes.isSuccess && runtimes.data.length === 0 && (
          <div role="alert" className="text-xs text-bad">
            No ACP runtimes are configured. Add one under <code>runtimes</code>{" "}
            in config.yaml.
          </div>
        )}
        <StarterPrompts kind={kind} targetId={targetId} />
      </div>
      <div className="border-t border-line p-3" data-chat-composer="">
        <Composer
          disabled={runtime === ""}
          placeholder={
            targetId === null
              ? cfg.composerPlaceholder.new
              : cfg.composerPlaceholder.existing
          }
          prefill={assist.seed}
          onPrefillApplied={assist.consumeSeed}
          autoFocus
          onSend={start}
          onNewChat={onNewChat}
        />
      </div>
    </>
  );
}
