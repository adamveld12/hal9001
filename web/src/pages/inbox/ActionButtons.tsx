import type { InboxAction } from "../../api/inboxActions";
import { Button } from "../../components/ui";

/** "Next steps": one button per action, the prompt as hover text. A used action opens the chat instead of running again. */
export function ActionButtons({
  actions,
  disabled,
  canOpenChat,
  onRun,
  onOpenChat,
}: {
  actions: readonly InboxAction[];
  /** An interaction is running or a request is in flight. */
  disabled: boolean;
  canOpenChat: boolean;
  onRun: (index: number) => void;
  onOpenChat: () => void;
}) {
  if (actions.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-xs font-medium text-muted">Next steps</span>
      {actions.map((a, i) => (
        <Button
          key={`${i}:${a.label}`}
          icon={a.used ? "chat" : "play"}
          title={a.used ? `Already run. Open chat.\n\n${a.prompt}` : a.prompt}
          aria-disabled={a.used || undefined}
          className={a.used ? "opacity-50" : undefined}
          disabled={a.used ? !canOpenChat : disabled}
          onClick={() => (a.used ? onOpenChat() : onRun(i))}
        >
          {a.label}
        </Button>
      ))}
    </div>
  );
}
