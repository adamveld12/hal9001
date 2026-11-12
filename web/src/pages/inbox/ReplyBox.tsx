import { useState } from "react";
import { Button, TextArea } from "../../components/ui";

/** Free-text reply to the agent of a note. Cmd/Ctrl+Enter sends; the text is cleared once the send succeeded. */
export function ReplyBox({
  disabled,
  pending,
  onSend,
}: {
  disabled: boolean;
  pending: boolean;
  /** Resolves true when the message was sent. */
  onSend: (text: string) => Promise<boolean>;
}) {
  const [text, setText] = useState("");
  const ready = !disabled && text.trim() !== "";
  const send = () => {
    if (!ready) return;
    void onSend(text).then((ok) => {
      if (ok) setText("");
    });
  };
  return (
    <div className="space-y-2">
      <TextArea
        rows={3}
        aria-label="Reply to agent"
        placeholder="Reply to agent…"
        value={text}
        disabled={disabled}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            send();
          }
        }}
      />
      <div className="flex items-center gap-2">
        <Button variant="primary" disabled={!ready} onClick={send}>
          {pending ? "Sending…" : "Send"}
        </Button>
        <span className="text-[11px] text-muted">Cmd/Ctrl+Enter sends</span>
      </div>
    </div>
  );
}
